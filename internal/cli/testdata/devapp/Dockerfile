# Tiny stand-in for a real app: busybox httpd serving /app on 8080.
FROM busybox:1.36 AS base
WORKDIR /app

FROM base AS dev
RUN echo dev > /stage
CMD ["sh", "-c", "echo houston-dev-started; exec httpd -f -v -p 8080 -h /app"]

FROM base AS test
RUN echo test > /stage

FROM base AS production
RUN echo production > /stage
COPY . /app
CMD ["httpd", "-f", "-p", "8080", "-h", "/app"]
