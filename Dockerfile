FROM golang:1.21-alpine
EXPOSE 8080
RUN apk add --update git
WORKDIR /go/go-rest-api
RUN go mod init go-rest-api && go get github.com/gorilla/mux@latest
COPY rest-api.go ./
RUN go build -o go-rest-api .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=0 /go/go-rest-api/go-rest-api .
CMD ["/app/go-rest-api"]   