# Host control probe

> Earlier experiment. This is separate from the
> [current Go CLI](../../../docs/cli.md).

Follow the [guided test](../../notes/host-control-test.md) for the two-account
Colima trial. The [handoff](../../notes/host-control-handoff.md) defines the full
boundary and remaining checks.

`prepare.py` creates a new external private bundle containing explicit public
sources and a synthetic ticket. `probe.py` targets only the generated endpoints
in the manager's bounded manifest. It distinguishes obtained access, confirmed
denials, unavailable checks and a same-UID control. It never declares the complete
protection boundary established.

Offline checks use generated fixtures only:

```sh
python3 -B -m unittest discover -s research/spikes/host-control -p 'test_*.py' -v
python3 -B -m unittest discover -s research/spikes/colima -p 'test_*.py' -v
bash research/spikes/colima/full_trial.sh --check
```

These commands do not start a VM, read personal authentication or provision
host accounts. Trial bundles, result files, generated keys and logs must remain
outside tracked source.
