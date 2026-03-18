all: cli larkbot

cli:
	go build -o bin/askplanner_cli ./cmd/askplanner

larkbot:
	go build -o bin/askplanner_larkbot ./cmd/larkbot

clean:
	rm -f bin/askplanner_cli bin/askplanner_larkbot

fmt:
	go fmt ./...

.PHONY: all cli larkbot clean fmt
