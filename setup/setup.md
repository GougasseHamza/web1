# Important Notes

## Environment Setup Requirements

The challenge is self-contained for an offline Ubuntu 26 AWS instance. The web UI uses only local assets, the runtime is a static Go binary, and the Docker image uses `FROM scratch`, so the image build does not need Docker Hub.

Run the commands below one block at a time from the repository root.

```bash
cd /home/ubuntu/secdojo
pwd
ls -la
```

Create the host flag file. Replace the value before publishing the lab.

```bash
printf '%s\n' 'SECdojo{REPLACE_WITH_REAL_FLAG}' | sudo tee /home/local.txt >/dev/null
sudo chmod 0644 /home/local.txt
sudo test -f /home/local.txt
sudo ls -l /home/local.txt
```

Build or verify the static challenge binary.

```bash
if command -v go >/dev/null 2>&1; then
  cd /home/ubuntu/secdojo/setup/tools
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ../bin/teamshelf .
  cd /home/ubuntu/secdojo
else
  test -x setup/bin/teamshelf
fi
file setup/bin/teamshelf
```

Validate the Compose file before starting the service.

```bash
docker compose -f setup/tools/docker-compose.yml config
```

Build the local image.

```bash
docker compose -f setup/tools/docker-compose.yml build
```

Start the challenge.

```bash
docker compose -f setup/tools/docker-compose.yml up -d
docker compose -f setup/tools/docker-compose.yml ps
```

Verify the public behavior.

```bash
curl -i http://127.0.0.1:8080/ | head
curl -i 'http://127.0.0.1:8080/admin?queue=upload-review' | head
printf '%s\n' 'not a pdf' > /tmp/fake.pdf
curl -s -F 'document=@/tmp/fake.pdf;filename=quarterly-report.pdf;type=application/pdf' http://127.0.0.1:8080/upload
```

Expected behavior:

- `/` returns the TeamShelf workspace UI.
- `/admin?queue=upload-review` returns `404 Not Found` from the edge gateway.
- invalid PDF uploads return a JSON notice pointing to `/admin?queue=upload-review`.
- `docker compose -f setup/tools/docker-compose.yml ps` shows `secdojo-teamshelf` running.

If the lab should listen on port 80 instead of 8080, set `PUBLIC_PORT` before starting Compose.

```bash
docker compose -f setup/tools/docker-compose.yml down
PUBLIC_PORT=80 docker compose -f setup/tools/docker-compose.yml up -d
docker compose -f setup/tools/docker-compose.yml ps
```

Enable Docker after reboot and keep the challenge container persistent.

```bash
sudo systemctl enable --now docker
docker update --restart unless-stopped secdojo-teamshelf
```

After a reboot, verify the service.

```bash
docker ps --filter name=secdojo-teamshelf
curl -s http://127.0.0.1:8080/api/health
```

The flag is mounted from the host path `/home/local.txt` into the container at the same path. This is required so the platform flag persists across container rebuilds and instance reboots. The Compose file uses `create_host_path: false` so startup fails clearly if `/home/local.txt` is missing instead of creating the wrong type of mount.

## Lab Environment Constraints

- No outbound internet is required during runtime.
- No npm, pip, apt, CDN, or remote font dependency is used by the challenge.
- Docker image build does not pull a base image because the Dockerfile uses `FROM scratch`.
- The included binary is linux/amd64. On arm64, install Go before deployment and build with `GOARCH=arm64`.
- Do not hardcode an instance IP in service config. Access the lab with `http://<instance-ip>:8080/` or the configured `PUBLIC_PORT`.

## File Organization

- Challenge runtime source: `setup/tools/main.go`, `setup/tools/web/`
- Docker deployment: `setup/tools/Dockerfile`, `setup/tools/docker-compose.yml`, `setup/bin/teamshelf`
- Documentation images: `writeup/images/`
