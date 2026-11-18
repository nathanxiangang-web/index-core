"""Analyze receiver/claim evidence without inferring requests from queue counts."""
import argparse
import collections
import datetime as dt
import json
import hashlib
import gzip
import math
import re
from pathlib import Path


def stamp(value):
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00'))


def quantiles(values, population=None):
    values = sorted(values)
    result = {'observed': len(values), 'p50': None, 'p95': None, 'p99': None, 'max': None}
    if population is not None:
        result.update(population=population, censored=population-len(values))
    if values:
        for p in (50, 95, 99):
            result['p'+str(p)] = values[math.ceil(len(values)*p/100)-1]
        result['max'] = values[-1]
    return result


def analyze(path):
    path = Path(path)
    data = gzip.decompress(path.read_bytes()) if path.suffix == '.gz' else path.read_bytes()
    records = [json.loads(line) for line in data.decode('utf-8').splitlines()]
    requests = [r['event'] for r in records if r['type'] == 'receiver_http']
    claims = [r['event'] for r in records if r['type'] == 'claim']
    results = [r['result'] for r in records if r['type'] == 'result']
    if not results:
        return {'status': 'INCOMPLETE', 'actual_receiver_requests': len(requests), 'path': str(path)}
    result = results[-1]
    assert result['wire_requests'] == len(requests), 'receiver totals disagree'
    starts = sorted(stamp(r['Started']) for r in requests)
    buckets = collections.Counter(math.floor((s-stamp(result['runtime_start'])).total_seconds()) for s in starts)
    lists = [r for r in requests if r['HTTP'] == '/api/fs/list']
    counts = collections.Counter(r['Scope'] for r in lists)
    statuses = collections.Counter(str(r['Status']) for r in requests)
    boundaries = []
    for r in requests:
        boundaries.extend([(stamp(r['Started']), 1), (stamp(r['Finished']), -1)])
    active = peak = 0
    for _, delta in sorted(boundaries, key=lambda x: (x[0], x[1])):
        active += delta
        peak = max(peak, active)
    assert active == 0
    normal = [(stamp(c['Claimed'])-stamp(c['Seen'])).total_seconds() for c in claims if c['Scope'] != '/']
    known = [c for c in claims if c['Scope'].startswith('/d')]
    intro = stamp(result['source_injected'])
    source_claim, observed_claim, child, directory = [], [], [], []
    for v in result['visibility'].values():
        if v['Claimed']:
            source_claim.append((stamp(v['Claimed'])-intro).total_seconds())
            if v.get('Discovered'):
                observed_claim.append((stamp(v['Claimed'])-stamp(v['Discovered'])).total_seconds())
        if v['FileVisible']:
            child.append((stamp(v['FileVisible'])-intro).total_seconds())
        if v['DirectoryVisible']:
            directory.append((stamp(v['DirectoryVisible'])-intro).total_seconds())
    samples = result['samples']
    stats = [s['Stats'] for s in samples]
    costs = result['query_costs']
    classes = collections.defaultdict(lambda: {'calls': 0, 'microseconds': 0})
    for q in costs:
        classes[q['class']]['calls'] += q['Calls']
        classes[q['class']]['microseconds'] += q['TotalMicroseconds']
    resources = path.parent / 'pg18-resources.ndjson'
    if not resources.exists() and path.name.endswith('.ndjson.gz'):
        stem, date = path.name.removesuffix('.ndjson.gz').rsplit('-', 1)
        resources = path.parent / (stem + '-pg-' + date + '.ndjson.gz')
    pg = []
    if resources.exists():
        raw = gzip.decompress(resources.read_bytes()) if resources.suffix == '.gz' else resources.read_bytes()
        pg = [json.loads(line) for line in raw.decode('utf-8').splitlines()]
    valid_pg = [p for p in pg if 'postgres' in p and 'container' in p]
    old_db = next((d for p in valid_pg for d in p['postgres']['databases'] if d['datname'] == 'index138_test'), None)
    new_db = next((d for p in reversed(valid_pg) for d in p['postgres']['databases'] if d['datname'] == 'index138_test'), None)
    delta = {k: new_db[k]-old_db[k] for k in ('xact_commit', 'xact_rollback', 'blks_read', 'blks_hit', 'temp_bytes', 'deadlocks')} if old_db and new_db else 'NOT_TRACKED'
    prior = Path(__file__).resolve().parents[2] / 'internal/store/postgres/prior.go'
    prior_cost = 'NOT_TRACKED_source_unavailable'
    if prior.exists():
        match = re.search(r'return `([^`]*)` \+ where \+ `([^`]*)`', prior.read_text(encoding='utf-8'), re.S)
        if match:
            fingerprint = hashlib.sha256((match[1]+'c.root_id=$1::uuid'+match[2]).encode()).hexdigest()[:16]
            found = next((q for q in costs if q['fingerprint'] == fingerprint), None)
            if found:
                prior_cost = {'source': 'internal/store/postgres/prior.go:priorQuery/LoadPriorResources', 'fingerprint': fingerprint, 'calls': found['Calls'], 'errors': found['Errors'], 'returned_rows': found['Rows'], 'client_seconds': found['TotalMicroseconds']/1e6}
    summary = {
        'mode': result['mode'], 'status': 'COLLECTED', 'capacity_verdict': result['capacity_verdict'],
        'population': result['population'], 'initial_pending': result['initial_pending'],
        'actual_receiver_requests': len(requests), 'actual_lists': len(lists),
        'http_statuses': dict(statuses), 'http429': statuses['429'],
        'http5xx': sum(n for c, n in statuses.items() if 500 <= int(c) < 600),
        'client_timeouts': result['client_timeouts'], 'exact_wire_start': result['exact_wire_start'],
        'anchored_receiver_arrivals_per_second': dict(sorted(buckets.items())),
        'receiver_peak_arrivals_per_second': max(buckets.values(), default=0),
        'receiver_peak_active_handlers': peak,
        'repeat_scope_fraction_including_intentional_hot_revisits': (len(lists)-len(counts))/len(lists) if lists else None,
        'source_introduction_to_claim_seconds': quantiles(source_claim, 12),
        'snapshot_first_observation_to_claim_seconds': quantiles(observed_claim, 12),
        'source_introduction_to_child_q5_q8_seconds': quantiles(child, 12),
        'source_introduction_to_directory_q5_seconds': quantiles(directory, 12),
        'observed_normal_wait_seconds': quantiles(normal),
        'known_401_claimed_scopes': len(set(c['Scope'] for c in known)),
        'normal_full_cohort_quantiles': 'RIGHT_CENSORED unless all401 claimed; observed quantiles are conditional',
        'managed_watch_first_last': [stats[0]['Watches'], stats[-1]['Watches']],
        'total_watch_rows_final': result.get('total_watch_rows', 'NOT_TRACKED'),
        'managed_poll_work_peak': max(s['ManagedPollWork'] for s in samples),
        'outstanding_root_peak': max(s['OutstandingRoots'] for s in stats),
        'watch_retirement_events': stats[-1]['RetirementEvents'] if result['mode'] == 'rolling' else 'NOT_TRACKED_legacy',
        'freshness_debt_seconds_first_last': [stats[0]['FreshnessDebtSeconds'], stats[-1]['FreshnessDebtSeconds']],
        'freshness_debt_growth_per_wall_second': (stats[-1]['FreshnessDebtSeconds']-stats[0]['FreshnessDebtSeconds'])/(stamp(samples[-1]['At'])-stamp(samples[0]['At'])).total_seconds(),
        'runtime_query_calls': sum(q['Calls'] for q in costs),
        'runtime_query_total_client_seconds': sum(q['TotalMicroseconds'] for q in costs)/1e6,
        'whole_root_prior_cost': prior_cost,
        'runtime_query_classes': dict(classes),
        'top_runtime_query_fingerprints': sorted(costs, key=lambda q: q['TotalMicroseconds'], reverse=True)[:10],
        'pg_resource_samples': len(valid_pg),
        'pg_cpu_percent_peak': max((float(p['container']['CPUPerc'].strip('%')) for p in valid_pg), default=None),
        'pg_memory_usage_observations': sorted(set(p['container']['MemUsage'] for p in valid_pg)),
        'pg_lock_waiters_sample_peak': max((p['postgres']['lock_waiters'] for p in valid_pg), default=None),
        'pg_counter_deltas_including_setup_and_observers': delta,
        'limitations': ['120s finite sample, not full20K traversal/steady state/soak', 'SQL-seeded historical identities/verification, live401 API enqueues', 'receiver arrival times are not exact client wire-starts', 'query elapsed is client wall time, not PostgreSQL CPU or pool acquisition', 'DB resource counters include fixtures, observers and receiver claim reads', 'point samples do not exclude unsampled lock waits'],
    }
    assert all(v >= 0 for v in normal+source_claim+observed_claim+child+directory)
    return summary


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('evidence', nargs='+')
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    summaries = [analyze(path) for path in args.evidence]
    Path(args.output).write_text(json.dumps(summaries, indent=2) + '\n', encoding='utf-8')
    for s in summaries:
        print(json.dumps({k: s.get(k, 'NOT_TRACKED') for k in ('mode', 'capacity_verdict', 'actual_receiver_requests', 'actual_lists', 'managed_poll_work_peak', 'freshness_debt_seconds_first_last', 'runtime_query_total_client_seconds')}))
