FROM busybox:1.37 AS base
COPY entry.sh /entry.sh
EXPOSE 80
CMD ["/entry.sh"]

FROM base AS dev
FROM base AS test
FROM base AS production
