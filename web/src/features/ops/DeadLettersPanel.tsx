import type { ReactNode } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api, type DeadLetterPage } from '../../api/client';
import { Panel, StateView } from '../../components/Panel';
import { fromQuery } from '../../lib/requestState';

/**
 * DeadLettersPanel lists events that will not be retried, with the context an
 * operator needs to act: what failed, when, how many attempts, and the
 * identifiers that join the entry to the logs.
 */
export function DeadLettersPanel(): ReactNode {
  const query = useQuery({
    queryKey: ['dead-letters'],
    queryFn: () => api.deadLetters(),
    refetchInterval: 5000,
  });
  const state = fromQuery<DeadLetterPage>(query, (page) => page.items.length === 0);

  return (
    <Panel title="Dead letters" id="dlq-panel">
      <StateView state={state} emptyMessage="Nothing has been dead lettered.">
        {(page) => (
          <table>
            <caption className="visually-hidden">
              Dead lettered events, newest failure first
            </caption>
            <thead>
              <tr>
                <th scope="col">Event</th>
                <th scope="col">Type</th>
                <th scope="col">Attempts</th>
                <th scope="col">Last failure</th>
                <th scope="col">Reason</th>
                <th scope="col">Correlation</th>
              </tr>
            </thead>
            <tbody>
              {page.items.map((entry) => (
                <tr key={entry.id}>
                  <th scope="row">
                    <code>{entry.event_id.slice(0, 8)}</code>
                  </th>
                  <td>{entry.event_type}</td>
                  <td>{entry.attempts}</td>
                  <td>
                    <time dateTime={entry.last_failed_at}>
                      {new Date(entry.last_failed_at).toLocaleString()}
                    </time>
                  </td>
                  <td>{entry.failure_reason}</td>
                  <td>
                    <code>{entry.correlation_id ?? ''}</code>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </StateView>
    </Panel>
  );
}
