#!/usr/bin/env bash
# Perplexity Computer (pplx CLI) entrypoint.
# Docs: https://docs.perplexity.ai/docs/cli/overview
set -e

if [[ -f /entrypoint-lib.sh ]]; then
  # shellcheck source=/dev/null
  source /entrypoint-lib.sh
fi

if command -v proveo-entrypoint >/dev/null 2>&1; then
  export PROVEO_SMOKE_TARGET=perplexity
  proveo-entrypoint prep perplexity || true
else
  ensure_runtime_user
  set_working_directory "/app"
  load_env
  bridge_git_identity
  report_git_context
  attach_rtk
fi

if [[ -z "${PERPLEXITY_API_KEY:-}" ]]; then
  echo "⚠️  PERPLEXITY_API_KEY not set. Create one at console.perplexity.ai,"
  echo "   or run 'pplx auth login' interactively (subscription login)."
  echo "   Prefer PERPLEXITY_API_KEY — login credentials are scrubbed from proveo home."
fi

# Utility / forwarded commands: run pplx with the given args.
if [[ $# -gt 0 ]]; then
  echo "🚀 Launching: pplx $*"
  exec pplx "$@"
fi

# No args: interactive shell so the user can `pplx auth login` then search.
echo "🚀 Perplexity Computer shell — try: pplx auth login"
echo "   then: pplx search web \"your query\""
exec bash
