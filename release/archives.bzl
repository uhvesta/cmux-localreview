"""Release archive helper shared by each supported platform target."""

def archive(name, cli, daemon, platform):
    native.genrule(
        name = name,
        srcs = [cli, daemon],
        outs = ["localreview_%s.tar.gz" % platform],
        cmd = "set -euo pipefail; root=\"$(@D)/localreview_%s\"; rm -rf \"$$root\"; mkdir -p \"$$root/bin\"; cp \"$(location %s)\" \"$$root/bin/localreview\"; cp \"$(location %s)\" \"$$root/bin/localreviewd\"; chmod 0755 \"$$root/bin/localreview\" \"$$root/bin/localreviewd\"; tar -C \"$(@D)\" -czf \"$@\" \"localreview_%s\"" % (platform, cli, daemon, platform),
        tags = ["manual"],
    )
