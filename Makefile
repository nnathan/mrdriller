mrdriller: main.go
	go build -o mrdriller

.PHONY: clean
clean:
	rm -f mrdriller
