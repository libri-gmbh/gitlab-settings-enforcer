FROM scalify/glide:0.13.1 as builder
WORKDIR /go/src/github.com/erinkerNCS/gitlab-settings-enforcer/

COPY glide.yaml glide.lock ./
RUN glide install --strip-vendor

COPY . ./
RUN CGO_ENABLED=0 go build -a -ldflags '-s' -installsuffix cgo -o bin/gitlab-project-settings-state-enforcer .


FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /go/src/github.com/erinkerNCS/gitlab-settings-enforcer/bin/gitlab-settings-enforcer .
RUN chmod +x gitlab-settings-enforcer
ENTRYPOINT ["/root/gitlab-settings-enforcer"]
