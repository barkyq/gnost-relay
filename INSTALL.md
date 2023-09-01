# Installation instructions

Debian instructions only. Ubuntu should be similar. Suppose that the user on the server is `barkyq`.

## Installing postgresql

1. Install `postgresql` by `sudo apt install postgresql`.

2. Run the following commands to give your user administrative rights, and create a database.

    ```zsh
    sudo -u postgres createuser --pwprompt barkyq
    sudo -u postgres createdb -O barkyq nostrdb
    ```
    Suppose the password you set is `super_secret_password`.

3. Check if `postgresql` is listening at `127.0.0.1:5432` by running `sudo netstat -tlpn` (install with `apt install net-tools`)

## Building gnost-relay
1. Download a recent version of `go` from https://go.dev/dl/. Follow the installation instructions.

2. Clone this repository.

3. Build the `gnost-relay` executable by running `go build .` in the directory you cloned the repository to.

4. Edit the `config.json` file. You need to change `relay_url` field if you want NIP-42 to work. You can also change the `nip11_info_document` field if you like.

5. Run the executable.

    ```zsh
    DATABASE_URL=postgres://barkyq:super_secret_password@localhost:5432/nostrdb ./gnost-relay
    ```
    The above command starts a relay listening at `localhost:8080` and sets the `DATABASE_URL` environment variable for the execution of the program.
    
## Installing NGINX and certbot for reverse proxy

1. Install `nginx` and `certbot`

    ```zsh
    sudo apt install nginx python3-certbot-nginx 
    ```
2. Download a SSL certificate (assuming you own `foo.bar`)

    ```zsh
    sudo certbot --nginx certonly -d relay.foo.bar
    ```
3. Copy the nginx configuration from `nginx.txt` to `/etc/nginx/sites-available/relay.foo.bar` and then make a symlink to this file in `/etc/nginx/sites-enabled/` by running:

    ```zsh
    cd /etc/nginx/sites-enabled/
    sudo ln -s ../sites-available/relay.foo.bar .
    ```
4. Reload `nginx` by running `sudo nginx -s reload` or restart `nginx` by running `sudo nginx restart`

5. If all went well, you should be able to connect to your relay at `wss://relay.foo.bar`

## External NGINX server

This section assumes you have an external NGINX server and need to point it to the relay.

1. Open the `config.json` file and change the `host` value to match the server's hostname or IP address and port.
    ```zsh
    "host": "192.168.1.2:8080"
    ```
    or
    ```zsh
    "host": "relay.example.local:1234"
    ```

2. Copy the nginx configuration from `nginx.txt` to `/etc/nginx/sites-available/relay.foo.bar` 

3. Edit your configuration file to change the `proxy_pass` variable to match the server hostname or IP and listening port.
    ```zsh
    proxy_pass 192.168.1.2:8080
    ```

4. Make a symlink to this file in `/etc/nginx/sites-enabled/` by running:

    ```zsh
    cd /etc/nginx/sites-enabled/
    sudo ln -s ../sites-available/relay.foo.bar .
    ```

5. Reload `nginx` but running `sudo nginx -s reload` or restart `nginx` by running `sudo nginx restart`

6. If all went well, you should be able to connect to your relay at `wss://relay.foo.bar`
