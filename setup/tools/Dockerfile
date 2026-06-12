FROM scratch

COPY bin/teamshelf /teamshelf

USER 65532:65532
EXPOSE 8080
ENV LISTEN_ADDR=:8080

ENTRYPOINT ["/teamshelf"]

