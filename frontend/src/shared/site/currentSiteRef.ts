// Lets SiteContext push the selected Site id into non-React code; it's the only writer.
import { setCurrentSiteId } from '../../constants';

export function publishSelectedSiteId(siteId: string): void {
  setCurrentSiteId(siteId);
}
