# TeamShelf Deployment Setup

Run these commands from the repository root. The host must already have Docker Engine with the Compose plugin installed.

```bash
ROOT="$(pwd)"
test -f "$ROOT/setup/tools/docker-compose.yml"
test -f "$ROOT/setup/tools/Dockerfile"
test -x "$ROOT/setup/bin/teamshelf"
```

## 1. Verify the Host Flag

The platform flag must exist on the host at `/home/local.txt` before the container starts. The Compose file mounts that exact file read-only into the container.

```bash
sudo test -s /home/local.txt
sudo chmod 0644 /home/local.txt
sudo ls -l /home/local.txt
```

## 2. Verify Docker

```bash
sudo systemctl enable --now docker
docker --version
docker compose version
```

## 3. Build or Verify the Binary

The repository includes a prebuilt linux/amd64 static binary at `setup/bin/teamshelf`. If Go is installed, rebuild it from source. If Go is not installed, the included binary is used.

```bash
cd "$ROOT"
if command -v go >/dev/null 2>&1; then
  (
    cd setup/tools
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ../bin/teamshelf .
  )
else
  test -x setup/bin/teamshelf
fi
file setup/bin/teamshelf
```

## 4. Validate Compose

```bash
cd "$ROOT"
docker compose -f setup/tools/docker-compose.yml config
```

## 5. Build the Image

```bash
cd "$ROOT"
docker compose -f setup/tools/docker-compose.yml build
```

## 6. Start the Service

```bash
cd "$ROOT"
docker compose -f setup/tools/docker-compose.yml up -d
docker compose -f setup/tools/docker-compose.yml ps
```

The default public port is `8080`. To use another port, set `PUBLIC_PORT` when starting Compose.

```bash
cd "$ROOT"
docker compose -f setup/tools/docker-compose.yml down
PUBLIC_PORT=80 docker compose -f setup/tools/docker-compose.yml up -d
docker compose -f setup/tools/docker-compose.yml ps
```

## 7. Verify Runtime Behavior

```bash
curl -i http://127.0.0.1:8080/ | head
curl -i 'http://127.0.0.1:8080/admin?queue=upload-review' | head
curl -s http://127.0.0.1:8080/api/health
printf '%s\n' 'not a pdf' > /tmp/fake.pdf
curl -s -F 'document=@/tmp/fake.pdf;filename=quarterly-report.pdf;type=application/pdf' http://127.0.0.1:8080/upload
```

Expected results:

- `/` returns the TeamShelf workspace UI.
- `/admin?queue=upload-review` returns `404 Not Found` from the edge gateway.
- `/api/health` returns JSON with `obj-eu-archive-03` and `http/1.1 keep-alive`.
- The invalid upload response includes `/admin?queue=upload-review`.

## 8. Verify Reboot Persistence

```bash
docker update --restart unless-stopped secdojo-teamshelf
```

Reboot the host when you are ready to test persistence. After the host comes back:

```bash
docker ps --filter name=secdojo-teamshelf
curl -s http://127.0.0.1:8080/api/health
sudo test -s /home/local.txt
```

## Notes

- Runtime does not require outbound internet.
- The web UI uses only local assets.
- The Dockerfile uses `FROM scratch`, so the image build does not pull a base image.
- `/home/local.txt` is mounted read-only with `create_host_path: false`; startup fails if the flag file is missing.
- On arm64 hosts, rebuild the binary with `GOARCH=arm64` before building the Docker image.
