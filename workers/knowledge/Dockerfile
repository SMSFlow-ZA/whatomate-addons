FROM golang@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY api/ ./api/
COPY governance/ ./governance/
COPY knowledge/ ./knowledge/
COPY documents/ ./documents/
COPY processing/ ./processing/
COPY workers/knowledge/ ./workers/knowledge/
RUN CGO_ENABLED=0 go build -trimpath -o /controller ./workers/knowledge
FROM docker@sha256:851f91d241214e7c6db86513b270d58776379aacc5eb9c4a87e5b47115e3065c
COPY --from=build /controller /usr/local/bin/knowledge-controller
ENTRYPOINT ["/usr/local/bin/knowledge-controller"]
