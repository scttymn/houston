# Houston builds the stage it needs: dev (houston dev), test (houston test),
# production (deploys). These defaults serve this folder as a static site;
# change them for your project.
FROM busybox:1.36 AS base
WORKDIR /app

FROM base AS dev
CMD ["httpd", "-f", "-v", "-p", "8080", "-h", "/app"]

FROM base AS test
COPY . /app

FROM base AS production
COPY . /app
CMD ["httpd", "-f", "-p", "8080", "-h", "/app"]
