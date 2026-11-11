"""Recompute metrics from recorded HTTP/Claim/Q5+Q8 events, never queue counts."""
import collections
import datetime
import json
import math
import pathlib
import sys


def instant(value):
    return datetime.datetime.fromisoformat(value.replace('Z', '+00:00')).timestamp()


def quantiles(values):
    if not values:
        return {'n': 0, 'status': 'NOT_TRACKED'}
    ordered = sorted(values)
    return dict(n=len(values), **{name: ordered[math.ceil(q * len(values))-1]
                                 for name, q in [('p50', .5), ('p95', .95), ('p99', .99), ('max', 1)]})


def analyze(path):
    with path.open(encoding='utf-8') as source:
        rows = [json.loads(line) for line in source if line.strip()]
    http = [row for row in rows if row['kind'] == 'http']
    claims = [row for row in rows if row['kind'] == 'claim']
    samples = [row for row in rows if row['kind'] == 'sample']
    summaries = [row for row in rows if row['kind'] == 'summary']
    workload = next(row for row in rows if row['kind'] == 'workload')
    start = instant(workload['start'])
    discovered = {row['scope']: instant(row['observed_at']) for row in rows if row['kind'] == 'discovered'}
    first_claim = {}
    normal_wait = []
    for row in claims:
        first_claim.setdefault(row['scope'], instant(row['claimed_at']))
        if row['scope'].startswith('/d'):
            normal_wait.append(instant(row['claimed_at']) - instant(row['first_seen_at']))
    visible = {row['path']: instant(row['at']) for row in rows if row['kind'] == 'visible'}
    claim_latencies = [first_claim[scope]-at for scope, at in discovered.items() if scope in first_claim]
    directory_visibility_latencies = [visible[scope]-at for scope, at in discovered.items() if scope in visible]
    visibility_latencies = [visible[scope+'/new.bin']-at for scope, at in discovered.items() if scope+'/new.bin' in visible]
    starts = sorted(instant(row['start']) for row in http)
    per_second = collections.Counter(math.floor(at-start) for at in starts)
    counts = collections.Counter(row['scope'] for row in claims)
    statuses = collections.Counter(str(row['status']) for row in http)
    duplicate_executions = sum(n-1 for n in counts.values())
    result = {
        'mode': workload['mode'], 'file': path.name, 'http_requests': len(http),
        'http_statuses': dict(statuses), 'http_timeouts': 'NOT_TRACKED',
        'http_starts_per_elapsed_second': dict(sorted(per_second.items())),
        'server_arrival_gap_seconds': quantiles([b-a for a, b in zip(starts, starts[1:])]),
        'http_peak_active': max((row['active_at_start'] for row in http), default=0),
        'list_executions': len(claims), 'repeated_scope_executions': duplicate_executions,
        'repeated_scope_ratio': duplicate_executions/len(claims) if claims else None,
        'distinct_claimed_scopes': len(counts), 'root_scope_executions': counts['/'],
        'root_scope_execution_ratio': counts['/']/len(claims) if claims else None,
        'new_directory_availability_to_first_observation_seconds':
            min(discovered.values())-instant(workload['new_directories_at']) if discovered else 'NOT_TRACKED',
        'normal_claim_wait_seconds': quantiles(normal_wait),
        'new_directory_to_claim_seconds': quantiles(claim_latencies),
        'new_directory_to_q5q8_seconds': quantiles(visibility_latencies),
        'new_directory_to_directory_q5q8_seconds': quantiles(directory_visibility_latencies),
        'new_directory_to_child_file_q5q8_seconds': quantiles(visibility_latencies),
        'new_directory_samples_expected': 12,
        'q5q8_visible_new_directories': sum(scope in visible for scope in discovered),
        'q5q8_visible_new_files': sum('/fresh' in p and p.endswith('/new.bin') for p in visible),
        'late_file_visible': '/d000000/late.bin' in visible,
        'watch_first': samples[0] if samples else 'NOT_TRACKED',
        'watch_last': samples[-1] if samples else 'NOT_TRACKED',
        'max_sampled_lock_waiters': max((s['lock_waiters'] for s in samples), default=None),
        'harness_summary': summaries[-1] if summaries else 'NOT_TRACKED',
    }
    if not discovered:
        result['discovery_timestamp_status'] = 'NOT_TRACKED in initial recorder; harness summary retained separately'
    result['freshness_verdict'] = 'FIX' if len(visibility_latencies) != 12 or not result['late_file_visible'] else 'MEASURED'
    return result


if __name__ == '__main__':
    directory = pathlib.Path(sys.argv[1])
    json.dump([analyze(directory/(mode+'.ndjson')) for mode in ('baseline', 'f1', 'f2') if (directory/(mode+'.ndjson')).exists()], sys.stdout, indent=2)
    print()
