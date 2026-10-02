#!/bin/sh
# Linux Docker iptables backend only. Install before starting the containers.
# Fixed addresses must match compose.yaml. Does not flush unrelated host rules.
set -eu
[ "$(id -u)" = 0 ] || { echo "Run as root" >&2; exit 1; }
iptables -w -S DOCKER-USER >/dev/null 2>&1 || iptables -w -N DOCKER-USER
command -v iptables-restore >/dev/null
command -v ip6tables >/dev/null
modprobe br_netfilter
sysctl -w net.bridge.bridge-nf-call-iptables=1
sysctl -w net.bridge.bridge-nf-call-ip6tables=1

# --noflush replaces only our named chains, in one IPv4 transaction.
iptables-restore --wait --noflush <<'RULES'
*filter
:SNATCHER-OUT - [0:0]
:SNATCHER-HOST - [0:0]
-F SNATCHER-OUT
-F SNATCHER-HOST
-A SNATCHER-OUT -m conntrack --ctstate ESTABLISHED --ctdir REPLY -j RETURN
-A SNATCHER-OUT -s 172.30.0.2 -d 172.30.0.3 -p tcp --dport 9000 -j RETURN
-A SNATCHER-OUT -d 0.0.0.0/8 -j REJECT
-A SNATCHER-OUT -d 10.0.0.0/8 -j REJECT
-A SNATCHER-OUT -d 100.64.0.0/10 -j REJECT
-A SNATCHER-OUT -d 127.0.0.0/8 -j REJECT
-A SNATCHER-OUT -d 169.254.0.0/16 -j REJECT
-A SNATCHER-OUT -d 172.16.0.0/12 -j REJECT
-A SNATCHER-OUT -d 192.0.0.0/24 -j REJECT
-A SNATCHER-OUT -d 192.0.2.0/24 -j REJECT
-A SNATCHER-OUT -d 192.88.99.0/24 -j REJECT
-A SNATCHER-OUT -d 192.168.0.0/16 -j REJECT
-A SNATCHER-OUT -d 198.18.0.0/15 -j REJECT
-A SNATCHER-OUT -d 198.51.100.0/24 -j REJECT
-A SNATCHER-OUT -d 203.0.113.0/24 -j REJECT
-A SNATCHER-OUT -d 224.0.0.0/4 -j REJECT
-A SNATCHER-OUT -d 240.0.0.0/4 -j REJECT
-A SNATCHER-OUT -p tcp -m multiport --dports 80,443 -j RETURN
-A SNATCHER-OUT -d 1.1.1.1 -p udp --dport 53 -j RETURN
-A SNATCHER-OUT -d 1.1.1.1 -p tcp --dport 53 -j RETURN
-A SNATCHER-OUT -j REJECT
-A SNATCHER-HOST -m conntrack --ctstate ESTABLISHED --ctdir REPLY -j ACCEPT
-A SNATCHER-HOST -j REJECT
COMMIT
RULES
iptables -w -C FORWARD -j DOCKER-USER 2>/dev/null ||
    iptables -w -I FORWARD 1 -j DOCKER-USER
iptables -w -C DOCKER-USER -i br-snatcher -j SNATCHER-OUT 2>/dev/null ||
    iptables -w -I DOCKER-USER 1 -i br-snatcher -j SNATCHER-OUT
iptables -w -C INPUT -i br-snatcher -j SNATCHER-HOST 2>/dev/null ||
    iptables -w -I INPUT 1 -i br-snatcher -j SNATCHER-HOST

# No IPv6 egress is needed for this IPv4-only deployment.
ip6tables -w -C FORWARD -i br-snatcher -j REJECT 2>/dev/null ||
    ip6tables -w -I FORWARD 1 -i br-snatcher -j REJECT
ip6tables -w -C INPUT -i br-snatcher -j REJECT 2>/dev/null ||
    ip6tables -w -I INPUT 1 -i br-snatcher -j REJECT
