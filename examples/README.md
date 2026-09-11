# Example input templates for docker-env-replace.
#
# Files in input/ are read-only inputs. Run the container with
#   DOCKER_ENV_REPLACE_INPUT_DIR=/input
#   DOCKER_ENV_REPLACE_OUTPUT_DIR=/output
# and the substituted copies appear in /output with the same tree
# structure. There are two demonstrations:
#
# 1. app.conf  - normal replacement. Every <NAME> token is replaced by
#    the value of the environment variable NAME.
#
# 2. optional.conf - explicit empty replacement. A variable that should
#    resolve to the empty string must be present and set to the empty
#    sentinel (default "(empty)") when the file is processed; the token
#    is then replaced by the empty string. A variable that is unset, or
#    that is set to the empty string, is an error (the run fails).

# Try it: create a `.env` file next to `docker-compose.yml` (the
# repository root) with the values below — the compose file reads it via
# `env_file`, so variables on the `docker compose` command line would
# not reach the container:
#
#   DB_HOST=db.example.com
#   DB_PORT=5432
#   TLS_ENABLED=(empty)
#
# then run:
#
#   docker compose up init
#
# produces /output containing:
#
#   app.conf:
#     host = db.example.com
#     port = 5432
#
#   optional.conf:
#     flagssl = l
#