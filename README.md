# pimgo

*paste · image · go*

paste image in browser → pimgo converts to avif and strips metadata → you get a static file link on your server

## Install & setup

1. [Download server binary](https://git.hloth.dev/hloth/pimgo/releases) *(10 MB)*
2. Run it, e.g `pimgo --listen 127.0.0.1:3000 --dir /srv/my-files/`
3. Configure your web server to proxy `/` → `127.0.0.1:3000`, rest → `/srv/my-files`

<details>
<summary>How to: serve with <b>Caddy</b> (recommended)</summary>

```Caddyfile
img.yourdomain.org {
	root /srv/my-files
	basic_auth / {
		# run `caddy hash-password`, replace HASH with the result, then login with "me" + the password
		me HASH
	}
	reverse_proxy / 127.0.0.1:3000
	# OR: reverse_proxy unix//run/pimgo.sock
	file_server
}
```

Want cache? Add this line line before `reverse_proxy`:

```Caddyfile
header /*.avif Cache-Control "public, max-age=31536000, immutable"
```

</details>

<details>
<summary>How to: serve with <b>nginx</b></summary>

Generate a password: `htpasswd -cB /etc/nginx/pimgo.htpasswd me`

> /etc/nginx/sites-available/img.yourdomain.org

```nginx
server {
	listen 443 ssl;
	server_name img.yourdomain.org;
	ssl_certificate     /etc/letsencrypt/live/img.yourdomain.org/fullchain.pem;
	ssl_certificate_key /etc/letsencrypt/live/img.yourdomain.org/privkey.pem;

	root /srv/my-files;

	location = / {
		auth_basic "pimgo";
		auth_basic_user_file /etc/nginx/pimgo.htpasswd;
		client_max_body_size 50m;

		proxy_pass http://127.0.0.1:3000;
		# OR: proxy_pass http://unix:/run/pimgo.sock;

		proxy_set_header Host $host;
		proxy_set_header X-Forwarded-Proto $scheme;
	}
}
```

Want cache? Add these lines before `location = / {`:

```nginx
location / {
	add_header Cache-Control "public, max-age=31536000, immutable";
}
```

</details>

<details>
<summary>How to: serve with <b>Apache</b></summary>

Generate a password: `htpasswd -cB /etc/apache2/pimgo.htpasswd me`

> /etc/apache2/sites-available/pimgo.conf

```apache
<VirtualHost *:443>
	ServerName img.yourdomain.org
	SSLEngine on
	SSLCertificateFile    /etc/letsencrypt/live/img.yourdomain.org/fullchain.pem
	SSLCertificateKeyFile /etc/letsencrypt/live/img.yourdomain.org/privkey.pem

	DocumentRoot /srv/my-files
	<Directory /srv/my-files>
		Options -Indexes
		AllowOverride None
		Require all granted
	</Directory>

	ProxyPreserveHost On
	RequestHeader set X-Forwarded-Proto https
	ProxyPassMatch "^/$" "http://127.0.0.1:3000/"
	# OR: ProxyPassMatch "^/$" "unix:/run/pimgo.sock|http://localhost/"

	<LocationMatch "^/$">
		AuthType Basic
		AuthName "pimgo"
		AuthUserFile /etc/apache2/pimgo.htpasswd
		Require valid-user
	</LocationMatch>

	AddType image/avif .avif
</VirtualHost>
```

Want cache? Add this line after `<Directory /srv/my-files>`:

```apache
Header set Cache-Control "public, max-age=31536000, immutable"
```

</details>

---


<details>
<summary>How to: run with <b>systemd</b></summary>

Create a user that owns the files: `useradd --system pimgo && chown pimgo /srv/my-files`

> /etc/systemd/system/pimgo.service

```ini
[Unit]
After=network.target

[Service]
ExecStart=/usr/local/bin/pimgo --listen 127.0.0.1:3000 --dir /srv/my-files
ReadWritePaths=/srv/my-files

User=pimgo
DynamicUser=yes
UMask=0022
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`systemctl daemon-reload && systemctl enable --now pimgo`

</details>

<details>
<summary>How to: run with systemd and <b>unix socket</b></summary>

> /etc/systemd/system/pimgo.socket

```ini
[Socket]
ListenStream=/run/pimgo.sock
SocketUser=root
SocketGroup=caddy
SocketMode=0660

[Install]
WantedBy=sockets.target
```

Create a user that owns the files: `useradd --system pimgo && chown pimgo /srv/my-files`

> /etc/systemd/system/pimgo.service

```ini
[Service]
ExecStart=/usr/local/bin/pimgo --dir /srv/my-files
ReadWritePaths=/srv/my-files

User=pimgo
DynamicUser=yes
UMask=0022
```

`systemctl daemon-reload && systemctl enable --now pimgo.socket`

</details>


## Build from source

Builds are reproducible.

```sh
CGO_ENABLED=0 GOTOOLCHAIN=go1.27.1 go build -trimpath -buildvcs=false -tags nodynamic -ldflags='-s -w'
```
