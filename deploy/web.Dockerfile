# The console is static files. It is built with node and served by nginx running
# as a non-root user, which is why the unprivileged image is used rather than the
# default one that starts as root and drops privileges afterwards.

FROM node:22-alpine AS build

WORKDIR /app

COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --fund=false

COPY web/ ./
COPY api/openapi.yaml /api/openapi.yaml

# The types are regenerated during the build. A console built from stale types
# would compile against a contract that no longer exists.
RUN npm run generate:api && npm run build

FROM nginxinc/nginx-unprivileged:1.27-alpine

COPY --from=build /app/dist /usr/share/nginx/html
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf

# 101 is the nginx user in the unprivileged image.
USER 101:101

EXPOSE 8080
