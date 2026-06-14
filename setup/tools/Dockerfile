FROM scratch

COPY bin/teamshelf /teamshelf
COPY rootfs/etc/passwd /etc/passwd
COPY rootfs/etc/group /etc/group

USER 65532:65532
EXPOSE 8080
ENV LISTEN_ADDR=:8080

ENTRYPOINT ["/teamshelf"]
