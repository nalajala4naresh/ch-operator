FROM alpine:3.24.4

RUN apk add --no-cache e2fsprogs

ADD build/virtmanager-container-rootfs-base/entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
