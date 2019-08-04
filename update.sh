#!/bin/bash
cd /var/www/src/commento
service commento stop
rm -rf /var/www/commento-server
git pull origin master
make prod
mv build/prod /var/www/commento-server
service commento start
