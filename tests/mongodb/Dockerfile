# Fake MongoDB log generator - writes synthetic mongod-style JSON log
# lines to the default path the MongoDB data source's collector config
# expects (/var/log/mongodb/mongod.log), so you can test log ingestion
# end-to-end without running a real mongod.
#
# See ../README.md for how to wire this up with the OTel Collector config
# generated from Obzly's Data Sources page.
FROM alpine:3.20

RUN apk add --no-cache bash coreutils

COPY generate-fake-logs.sh /usr/local/bin/generate-fake-logs.sh
RUN chmod +x /usr/local/bin/generate-fake-logs.sh

# Overridable: LOG_PATH, LOG_INTERVAL_SECONDS.
ENTRYPOINT ["/usr/local/bin/generate-fake-logs.sh"]
