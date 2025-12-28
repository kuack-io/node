FROM gcr.io/distroless/cc-debian12:nonroot

COPY --chmod=755 kuack-node /kuack-node

ENTRYPOINT ["/kuack-node"]
