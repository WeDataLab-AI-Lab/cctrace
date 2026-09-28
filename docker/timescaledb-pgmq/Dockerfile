# Pinned by digest, not by tag. `latest-pg16` moves, so two builds of this file
# a month apart produce different database images with nothing recording which
# one a deployment runs -- prod ran a five-month-old image while this file
# claimed to describe the current one. 2.25.2 is the version production runs
# (SELECT extversion FROM pg_extension WHERE extname = 'timescaledb').
#
# To move this pin: pick the new tag, read its digest, and replace BOTH halves of
# the line -- a tag without a digest is not a pin, and a digest without a tag
# leaves no record of which release it is.
#
#   docker buildx imagetools inspect timescale/timescaledb:<tag>
#
# Reads the registry manifest only, so it does not download layers. Take the
# top-level `Digest:` value (the index), not a per-platform manifest digest --
# the per-platform one pins one architecture and fails to pull on the others.
# After rebuilding, confirm the running extension version matches the tag with
# the SELECT above; the image tag and the extension version have disagreed before.
FROM timescale/timescaledb:2.25.2-pg16@sha256:0af03ecf697825f6ddae76fd275d16bf46007bed6d00eb3d754779cb7db96fa6

# The pgmq build was a shallow clone of the default branch: whatever sat on HEAD
# at build time, unrecorded. This is v1.11.0, verified against
# github.com/tembo-io/pgmq refs/tags/v1.11.0.
#
# To move this pin, resolve the tag to its commit rather than copying a SHA from
# the web UI:
#
#   git ls-remote https://github.com/tembo-io/pgmq refs/tags/v<X.Y.Z>
#
# pgmq uses lightweight tags, so that prints the commit directly. If upstream
# ever switches to annotated tags the same command returns the tag object, and
# `refs/tags/v<X.Y.Z>^{}` is then the commit -- check which one you got before
# pasting it here. Record the version in the comment above so the SHA stays
# readable; a bare SHA tells the next person nothing.
ARG PGMQ_COMMIT=ae5d34e096608666816d207d0c4b0342e0d58b64

RUN apk add --no-cache git build-base postgresql16-dev && \
    cd /tmp && \
    git clone https://github.com/tembo-io/pgmq.git && \
    cd pgmq && git checkout "${PGMQ_COMMIT}" && \
    cd pgmq-extension && \
    make && make install && \
    cd / && rm -rf /tmp/pgmq && \
    apk del git build-base postgresql16-dev
