#!/usr/bin/env bash
# egress-firewalld.sh — durable egress allowlist for the coding-agent network
# on firewalld hosts (Fedora/RHEL), WI-315.
#
# Agents only need DNS + the Windshift API host: LLM and git traffic is
# brokered through the orchestrator's proxies, so everything else outbound
# from the agent network can be rejected. The README's iptables sketch does
# not survive reboots or docker restarts; this script encodes the same
# allowlist as a permanent firewalld POLICY keyed on the network's SOURCE
# SUBNET (not the bridge interface), so it keeps holding even when docker
# recreates the bridge.
#
# Usage (as root, idempotently re-runnable):
#   sudo ./egress-firewalld.sh --allow windshift.example.com [--allow other.host]
#       [--network coding-agent-egress] [--policy codingAgentEgress] [--no-dns]
#
# Allowed hosts are resolved to A records AT APPLY TIME. Re-run the script
# when an allowed host's IP changes (or cron it).
set -euo pipefail

NETWORK=coding-agent-egress
POLICY=codingAgentEgress
ALLOW_DNS=1
ALLOW_HOSTS=()

die() { echo "egress-firewalld: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --allow)   ALLOW_HOSTS+=("${2:?}"); shift 2 ;;
    --network) NETWORK="${2:?}"; shift 2 ;;
    --policy)  POLICY="${2:?}"; shift 2 ;;
    --no-dns)  ALLOW_DNS=0; shift ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *)         die "unknown option: $1 (try --help)" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "must run as root"
[ ${#ALLOW_HOSTS[@]} -gt 0 ] || die "at least one --allow <host> is required (the Windshift API host)"
command -v firewall-cmd >/dev/null 2>&1 || die "firewall-cmd not found (this script targets firewalld hosts)"
firewall-cmd --state >/dev/null 2>&1 || die "firewalld is not running"
command -v docker >/dev/null 2>&1 || die "docker not found"

SUBNET="$(docker network inspect "$NETWORK" -f '{{range .IPAM.Config}}{{.Subnet}}{{end}}' 2>/dev/null)" \
  || die "docker network $NETWORK not found (the runner auto-creates it on first start)"
[ -n "$SUBNET" ] || die "could not determine the subnet of $NETWORK"
echo "==> $NETWORK subnet: $SUBNET"

# Resolve the allowlist to IPv4 addresses now; firewalld rules match IPs.
ALLOW_IPS=()
for host in "${ALLOW_HOSTS[@]}"; do
  if [[ "$host" =~ ^[0-9.]+$ ]]; then
    ips="$host"
  else
    ips="$(getent ahostsv4 "$host" 2>/dev/null | awk '{print $1}' | sort -u)"
    [ -n "$ips" ] || die "could not resolve $host"
  fi
  for ip in $ips; do
    echo "==> allow $host -> $ip"
    ALLOW_IPS+=("$ip")
  done
done

# Fresh policy each run so removed hosts actually disappear (idempotent).
if firewall-cmd --permanent --get-policies 2>/dev/null | tr ' ' '\n' | grep -qx "$POLICY"; then
  echo "==> replacing existing policy $POLICY"
  firewall-cmd --permanent --delete-policy "$POLICY" >/dev/null
fi
firewall-cmd --permanent --new-policy "$POLICY" >/dev/null

# Scope: forwarded traffic leaving the host. Prefer the docker zone (where
# docker's firewalld integration places its bridges); fall back to ANY — the
# rules below are source-scoped to the agent subnet either way, so other
# docker networks and host traffic are untouched.
INGRESS=ANY
if firewall-cmd --permanent --get-zones | tr ' ' '\n' | grep -qx docker; then
  INGRESS=docker
fi
firewall-cmd --permanent --policy "$POLICY" --add-ingress-zone "$INGRESS" >/dev/null
firewall-cmd --permanent --policy "$POLICY" --add-egress-zone ANY >/dev/null

# Rich-rule priorities make ordering explicit: accepts (-10) before the
# subnet-wide reject (10).
if [ "$ALLOW_DNS" -eq 1 ]; then
  for proto in udp tcp; do
    firewall-cmd --permanent --policy "$POLICY" --add-rich-rule \
      "rule priority=\"-10\" family=\"ipv4\" source address=\"$SUBNET\" port port=\"53\" protocol=\"$proto\" accept" >/dev/null
  done
fi
for ip in "${ALLOW_IPS[@]}"; do
  for port in 443 80; do
    firewall-cmd --permanent --policy "$POLICY" --add-rich-rule \
      "rule priority=\"-10\" family=\"ipv4\" source address=\"$SUBNET\" destination address=\"$ip/32\" port port=\"$port\" protocol=\"tcp\" accept" >/dev/null
  done
done
firewall-cmd --permanent --policy "$POLICY" --add-rich-rule \
  "rule priority=\"10\" family=\"ipv4\" source address=\"$SUBNET\" reject" >/dev/null

firewall-cmd --reload >/dev/null
echo "==> policy $POLICY applied (ingress zone: $INGRESS)"
echo
echo "Verify from inside the agent network:"
echo "  docker run --rm --network $NETWORK alpine \\"
echo "    sh -c 'wget -q -T 5 -O- https://${ALLOW_HOSTS[0]}/api/version && ! wget -q -T 5 -O- https://example.com'"
