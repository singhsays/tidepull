# Built by GoReleaser's dockers_v2: the build context holds the release
# binary for each platform under $TARGETPLATFORM. The image defaults to
# nonroot; run with a user matching the local directory's ownership.
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/tidepull /tidepull
ENTRYPOINT ["/tidepull"]
