# One Dockerfile for both Go binaries. The target is chosen with --build-arg, so
# the build stage is defined once and both images stay identical in everything
# except which binary they run.

FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies are their own layer, so a code change does not re-download the
# module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG BINARY=api
# CGO is off so the binary is static and the runtime image can be distroless.
# trimpath removes local paths from the binary, which would otherwise leak the
# build machine's directory layout into stack traces.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${BINARY} \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

# Distroless static: no shell, no package manager, no libc. Nothing to exploit
# that is not the application itself, and nothing to update on a base image
# advisory that does not affect the application.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/service /service
COPY --from=build /out/healthcheck /healthcheck

# The nonroot variant runs as uid 65532. It is stated explicitly so that a change
# of base image cannot silently promote the container to root.
USER 65532:65532

ENTRYPOINT ["/service"]
