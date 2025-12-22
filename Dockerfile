FROM gcr.io/distroless/cc-debian12:nonroot

COPY --chmod=755 kuack-node /kuack-node

EXPOSE 4433/udp
EXPOSE 10250/tcp

ENTRYPOINT ["/kuack-node"]
