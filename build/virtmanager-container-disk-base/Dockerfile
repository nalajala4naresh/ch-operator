FROM alpine:3.24.4

RUN apk add --no-cache qemu-img

ADD build/virtmanager-container-disk-base/entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
