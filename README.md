# gnost-relay
nostr relay written in `go`

Still alpha and logging is done directly to stdout so it might be a bit messy to run currently. 

# Prerequisite: postgresql

Debian instructions only. Ubuntu should be similar. Suppose that the user on the server is `barkyq`.

1. Install `postgresql` by `sudo apt install postgresql`.

2. Run the following commands to give your user administrative rights, and create a database:
```
sudo -u postgres createuser --pwprompt barkyq
sudo -u postgres createdb -O barkyq nostrdb
```
Suppose that the password you entered is `super_secret_password`.

3. Check if `postgresql` is listening at `127.0.0.1:5432` by running `sudo netstat -tlpn` (which you can install with `apt install net-tools`)

# Installing gnost-relay
1. Download a recent version of `go` from https://go.dev/dl/. Follow the installation instructions.

2. Clone this repository.

3. Edit the `config.go` file. The most important variable to change is the `relay_url` variable in `config.go`. You can also change the `nip11_info_document` variable if you like.

4. Build the `gnost-relay` executable by running `go build .` in the directory you cloned the repository to.

5. Run the executable by entering:
```
DATABASE_URL=postgres://barkyq:super_secret_password@localhost:5432/nostrdb ./gnost-relay
```
on the command line. This starts a relay listening at `localhost:8080`. This sets the `DATABASE_URL` environment variable for the execution of the program.

# Installing NGINX and certbot for reverse proxy
1. Install `nginx` and `certbot`
```
sudo apt install nginx python3-certbot-nginx 
```
2. Run:
```
sudo certbot --nginx certonly -d relay.foo.bar
```
to download a SSL certificate (assuming you own `foo.bar`).

3. Copy the nginx configuration below to `/etc/nginx/sites-available/relay.foo.bar` and then make a symlink to this file in `/etc/sites-enabled/` by running:
```
cd /etc/nginx/sites-enabled/
sudo ln -s ../sites-available/relay.foo.bar .
```
4. Restart `nginx` by running `sudo nginx -s reload`

5. If all went well, you should be able to connect to your relay at `wss://relay.foo.bar`

# NGINX configuration
```conf
server {
        server_name relay.foo.bar;

        location / {
                proxy_pass http://localhost:8080;
                proxy_http_version 1.1;
                proxy_set_header Upgrade $http_upgrade;
                proxy_set_header Connection "Upgrade";
                proxy_set_header Host $host;
                proxy_read_timeout 3600;
                proxy_send_timeout 3600;
        }

        listen 443 ssl http2;
        listen [::]:443 ssl http2;
        ssl_certificate /etc/letsencrypt/live/relay.foo.bar/fullchain.pem;
        ssl_certificate_key /etc/letsencrypt/live/relay.foo.bar/privkey.pem;
        include /etc/letsencrypt/options-ssl-nginx.conf;
        ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
}
```
