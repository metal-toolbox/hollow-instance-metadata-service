FROM alpine:3.21
RUN apk add --no-cache ca-certificates

FROM scratch
# Copy ca-certs from alpine
COPY --from=alpine /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Run as a non-root user (>10000 to avoid conflicts with the host's user table)
USER 10001:10001

# Copy the binary that goreleaser built
COPY metadataservice /metadataservice

# Run the web service on container startup.
ENTRYPOINT ["/metadataservice"]
CMD ["serve"]
