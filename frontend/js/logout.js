(function (global, document) {
  "use strict";

  global.logout = function() {
    global.cookieDelete("commentoOwnerToken");
	var urlParams = new URLSearchParams(window.location.search);
	var redirect = urlParams.get('redirect');
	if(redirect){
		document.location = redirect;
		return;
	}
    document.location = global.origin + "/login";
  }

} (window.commento, document));
