#!/usr/bin/env bash
# Seeds the Linear sandbox team with a realistic support/ops backlog so the
# agent under test has genuine, un-triaged work to act on. Idempotency is not
# attempted here on purpose: this is a one-shot fixture generator for a
# throwaway sandbox. Re-running it simply adds another batch.
#
# Usage: scripts/seed_linear.sh
# Requires LINEAR_API_KEY and LINEAR_TEAM_ID in ../.env
set -euo pipefail

cd "$(dirname "$0")/.."
KEY=$(grep '^LINEAR_API_KEY=' .env | cut -d= -f2-)
TEAM=$(grep '^LINEAR_TEAM_ID=' .env | cut -d= -f2-)

# title|priority  (priority: 1=urgent 2=high 3=normal 4=low). All left unassigned.
ISSUES=(
  "Customer unable to reset password|1"
  "Billing webhook failing for EU customers|1"
  "Mobile app crash on profile screen|1"
  "Export to CSV times out on large accounts|2"
  "Onboarding email not sending|2"
  "Dashboard shows stale metrics after refresh|2"
  "Duplicate invoices generated on retry|2"
  "API rate limit errors on bulk import|3"
  "Search returns no results for tagged items|3"
  "Slack notifications delayed by hours|3"
  "Add bulk-assign action to tickets|3"
  "Timezone wrong on scheduled reports|4"
  "Password reset link expires too quickly|2"
  "Notifications missing for mentions|3"
  "Account deletion request stuck in queue|4"
)

echo "Seeding ${#ISSUES[@]} issues into team $TEAM ..."
for row in "${ISSUES[@]}"; do
  title="${row%|*}"
  priority="${row#*|}"
  resp=$(curl -sS -X POST https://api.linear.app/graphql \
    -H "Authorization: $KEY" \
    -H "Content-Type: application/json" \
    -d "{\"query\":\"mutation(\$teamId:String!,\$title:String!,\$priority:Int){issueCreate(input:{teamId:\$teamId,title:\$title,priority:\$priority}){success issue{identifier title}}}\",\"variables\":{\"teamId\":\"$TEAM\",\"title\":\"$title\",\"priority\":$priority}}")
  ident=$(echo "$resp" | grep -o '"identifier":"[^"]*"' | head -1 | cut -d'"' -f4)
  echo "  created ${ident:-??}  $title"
done
echo "Done."
