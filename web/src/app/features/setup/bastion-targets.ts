import { Bastion, BastionsApi } from '../../api/bastions-api';
import { SessionStore } from '../../core/session-store';

/**
 * The Bastions the client setup and the guides offer as a place to install from (R-BAS-007, R-UI-049):
 * the registrations that have reported a base URL. Reading them needs `endpoints:read` (R-BAS-004); a
 * caller without it, or a list that cannot be read, gets no choice and no error, because the choice is
 * an offer: a Bastion named in the query of the page still works, the setup operation needs only `read`
 * on the repository (Q-BAS-96).
 */
export async function reportedBastions(api: BastionsApi, session: SessionStore): Promise<readonly Bastion[]> {
  if (!session.has('endpoints:read')) return [];
  try {
    return (await api.list()).filter((b) => b.contacts.some((c) => c.baseUrl !== ''));
  } catch {
    return [];
  }
}
