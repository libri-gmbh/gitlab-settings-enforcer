FROM golang:1-alpine as builder
WORKDIR /go/src/github.com/libri-gmbh/gitlab-settings-enforcer/

RUN export CGO_ENABLED=0 && \
    go get && \
    go build -a -ldflags '-s' -installsuffix cgo -o bin/gitlab-settings-enforcer .


FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /go/src/github.com/libri-gmbh/gitlab-settings-enforcer/bin/gitlab-settings-enforcer .
RUN chmod +x gitlab-settings-enforcer
ENTRYPOINT ["/root/gitlab-settings-enforcer"]
