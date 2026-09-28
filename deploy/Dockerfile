# Build the Linux binary first with scripts/build.sh. No dependency downloads are needed here.
FROM scratch
COPY dist/linux-amd64/remote-server /remote-server
USER 65532:65532
EXPOSE 8443/tcp 3478/udp
ENTRYPOINT ["/remote-server"]
CMD ["run","--config","/state/server.json"]
