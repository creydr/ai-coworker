FROM alpine:3.21
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh && adduser -D sandbox
USER sandbox
ENTRYPOINT ["/entrypoint.sh"]
