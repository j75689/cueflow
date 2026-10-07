# SearXNG with CueFlow's settings baked in, for platforms that cannot mount a file (Cloud Run).
# docker compose does not use this: it mounts settings.yml over the stock image.
#
# The settings enable the JSON output CueFlow reads. The instance secret is not here: set
# SEARXNG_SECRET in the environment.
FROM searxng/searxng:latest
COPY settings.yml /etc/searxng/settings.yml
