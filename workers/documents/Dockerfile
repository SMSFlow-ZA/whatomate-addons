# Required: immutable image references containing @sha256:, and exact APK versions.
# No default floating base or package versions are provided.
ARG GO_IMAGE
ARG RUNTIME_IMAGE
FROM ${GO_IMAGE} AS build
ARG GO_IMAGE
RUN case "$GO_IMAGE" in *@sha256:*) ;; *) exit 1 ;; esac
WORKDIR /src
COPY go.mod go.sum ./
COPY knowledge/ ./knowledge/
COPY documents/ ./documents/
COPY workers/documents/ ./workers/documents/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/document-worker ./workers/documents

FROM ${RUNTIME_IMAGE}
ARG RUNTIME_IMAGE
ARG CLAMAV_VERSION
ARG POPPLER_VERSION
ARG TESSERACT_VERSION
ARG TESSERACT_ENG_VERSION
ARG FONT_DEJAVU_VERSION
RUN case "$RUNTIME_IMAGE" in *@sha256:*) ;; *) exit 1 ;; esac \
 && test -n "$CLAMAV_VERSION" && test -n "$POPPLER_VERSION" \
 && test -n "$TESSERACT_VERSION" && test -n "$TESSERACT_ENG_VERSION" && test -n "$FONT_DEJAVU_VERSION" \
 && apk add --no-cache "clamav=$CLAMAV_VERSION" "poppler-utils=$POPPLER_VERSION" \
      "tesseract-ocr=$TESSERACT_VERSION" "tesseract-ocr-data-eng=$TESSERACT_ENG_VERSION" "font-dejavu=$FONT_DEJAVU_VERSION" \
 && addgroup -g 10001 worker && adduser -D -H -u 10001 -G worker worker
COPY --from=build /out/document-worker /usr/local/bin/document-worker
ENV GOMAXPROCS=2 OMP_THREAD_LIMIT=1
USER 10001:10001
WORKDIR /tmp
ENTRYPOINT ["/usr/local/bin/document-worker"]
