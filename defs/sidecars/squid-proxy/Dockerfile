FROM ubuntu/squid:latest

COPY squid.conf /etc/squid/squid.conf
COPY firehol-blocked-nets.conf /etc/squid/firehol-blocked-nets.conf
COPY firehol-ipset.conf /etc/squid/firehol-ipset.conf
COPY update-firehol-ipsets.sh /usr/local/bin/update-firehol-ipsets
RUN chmod +x /usr/local/bin/update-firehol-ipsets

EXPOSE 3128

CMD ["squid", "-f", "/etc/squid/squid.conf", "-NYC"]
