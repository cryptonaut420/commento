package main

import (
	"net/http"
	"time"
	"os"
	"net/url"
	"encoding/json"
	"encoding/hex"
	"crypto/hmac"
	"crypto/sha256"
)

// Take `creationDate` as a param because comment import (from Disqus, for
// example) will require a custom time.
func commentNew(commenterHex string, domain string, path string, parentHex string, markdown string, state string, creationDate time.Time) (string, error) {
	// path is allowed to be empty
	if commenterHex == "" || domain == "" || parentHex == "" || markdown == "" || state == "" {
		return "", errorMissingField
	}

	p, err := pageGet(domain, path)
	if err != nil {
		logger.Errorf("cannot get page attributes: %v", err)
		return "", errorInternal
	}

	if p.IsLocked {
		return "", errorThreadLocked
	}

	commentHex, err := randomHex(32)
	if err != nil {
		return "", err
	}

	html := markdownToHtml(markdown)

	statement := `
		INSERT INTO
		comments (commentHex, domain, path, commenterHex, parentHex, markdown, html, creationDate, state)
		VALUES   ($1,         $2,     $3,   $4,           $5,        $6,       $7,   $8,           $9   );
	`
	_, err = db.Exec(statement, commentHex, domain, path, commenterHex, parentHex, markdown, html, creationDate, state)
	if err != nil {
		logger.Errorf("cannot insert comment: %v", err)
		return "", errorInternal
	}

	if err = pageNew(domain, path); err != nil {
		return "", err
	}

	return commentHex, nil
}

func commentNewHandler(w http.ResponseWriter, r *http.Request) {
	type request struct {
		CommenterToken *string `json:"commenterToken"`
		Domain         *string `json:"domain"`
		Path           *string `json:"path"`
		ParentHex      *string `json:"parentHex"`
		Markdown       *string `json:"markdown"`
	}
	
	type permJson struct {
		Requester string `json:"requester"`
		Email     string `json:"email"`
		Route     string `json:"route"`
		PermKey   string `json:"permKey"`
	}
	
	type permResult struct {
		Result  bool `json:"result"`
		Error  *string `json:"error"`
	}

	var x request
	if err := bodyUnmarshal(r, &x); err != nil {
		bodyMarshal(w, response{"success": false, "message": err.Error()})
		return
	}

	domain := domainStrip(*x.Domain)
	path := *x.Path

	d, err := domainGet(domain)
	if err != nil {
		bodyMarshal(w, response{"success": false, "message": err.Error()})
		return
	}

	if d.State == "frozen" {
		bodyMarshal(w, response{"success": false, "message": errorDomainFrozen.Error()})
		return
	}

	if d.RequireIdentification && *x.CommenterToken == "anonymous" {
		bodyMarshal(w, response{"success": false, "message": errorNotAuthorised.Error()})
		return
	}

	// logic: (empty column indicates the value doesn't matter)
	// | anonymous | moderator | requireIdentification | requireModeration | moderateAllAnonymous | approved? |
	// |-----------+-----------+-----------------------+-------------------+----------------------+-----------|
	// |       yes |           |                       |                   |                   no |       yes |
	// |       yes |           |                       |                   |                  yes |        no |
	// |        no |       yes |                       |                   |                      |       yes |
	// |        no |        no |                       |               yes |                      |       yes |
	// |        no |        no |                       |                no |                      |        no |

	var commenterHex string
	var state string

	if *x.CommenterToken == "anonymous" {
		commenterHex = "anonymous"
		if isSpam(*x.Domain, getIp(r), getUserAgent(r), "Anonymous", "", "", *x.Markdown) {
			state = "flagged"
		} else {
			if d.ModerateAllAnonymous || d.RequireModeration {
				state = "unapproved"
			} else {
				state = "approved"
			}
		}
	} else {
		c, err := commenterGetByCommenterToken(*x.CommenterToken)
		if err != nil {
			bodyMarshal(w, response{"success": false, "message": err.Error()})
			return
		}

		// cheaper than a SQL query as we already have this information
		isModerator := false
		for _, mod := range d.Moderators {
			if mod.Email == c.Email {
				isModerator = true
				break
			}
		}

		commenterHex = c.CommenterHex

		if isModerator {
			state = "approved"
		} else {
			if isSpam(*x.Domain, getIp(r), getUserAgent(r), c.Name, c.Email, c.Link, *x.Markdown) {
				state = "flagged"
			} else {
				if d.RequireModeration {
					state = "unapproved"
				} else {
					state = "approved"
				}
			}
		}
		
		//check permissions with parent application
		var check_data permJson
		check_data.Requester = "commento"
		check_data.Email = c.Email
		check_data.Route = path
		check_data.PermKey = "canComment"
		check_json, err := json.Marshal(check_data)
		secret_bytes, err := hex.DecodeString(os.Getenv("PARENT_APP_API_SECRET"))
		h := hmac.New(sha256.New, secret_bytes)
		h.Write(check_json)
		SignatureBytes := h.Sum(nil)
		hmac_string := hex.EncodeString(string(SignatureBytes))

		resp, err := http.PostForm(os.Getenv("PARENT_APP_URL") + "/api/v1/permissions/check",
			url.Values{"requester": {"commento"}, "email": {c.Email}, "route": {path}, "permKey": {"canComment"}, "hmac": {hmac_string}})
		
		if err != nil {
			bodyMarshal(w, response{"success": false, "message": err.Error()})
			return	
		}
		
		var response_data permResult
		decode_response := json.NewDecoder(resp.Body)
		decode_response.Decode(&response_data)
		
		if response_data.Result == false {
			bodyMarshal(w, response{"success": false, "message": "permission denied"})
			return	
		}
	}

	commentHex, err := commentNew(commenterHex, domain, path, *x.ParentHex, *x.Markdown, state, time.Now().UTC())
	if err != nil {
		bodyMarshal(w, response{"success": false, "message": err.Error()})
		return
	}

	// TODO: reuse html in commentNew and do only one markdown to HTML conversion?
	html := markdownToHtml(*x.Markdown)

	bodyMarshal(w, response{"success": true, "commentHex": commentHex, "state": state, "html": html})
	if smtpConfigured {
		go emailNotificationNew(d, path, commenterHex, commentHex, *x.ParentHex, state)
	}
}
