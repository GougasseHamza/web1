.PHONY: build run stop logs clean

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=$${GOARCH:-amd64} go build -trimpath -ldflags "-s -w" -o ../bin/teamshelf .

run: build
	docker compose up -d --build

stop:
	docker compose down

logs:
	docker compose logs -f teamshelf

clean:
	rm -f ../bin/teamshelf
