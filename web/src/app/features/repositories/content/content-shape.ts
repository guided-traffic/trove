/**
 * What a repository's content is called and what it shows, per format and, for the words that differ, per class (R-UI-052). One shell serves every format; the words and
 * the columns are this small table, not a screen per format.
 */
export interface ContentShape {
  /** The tab and the page's content heading: Images, Packages or Files. */
  readonly itemsLabel: string;
  /** The name column of an item: Image, Package. */
  readonly itemLabel: string;
  /** The column that counts an item's versions: Tags, Versions. */
  readonly versionsLabel: string;
  /** The column of one version: Tag, Version. */
  readonly versionLabel: string;
  readonly filterLabel: string;
  readonly emptyText: string;
  readonly emptyFilteredText: string;
  /** The date a version arrived: Pushed for an image tag, Fetched for a tag of a remote repository, Published elsewhere. */
  readonly publishedLabel: string;
  /** A version has a content digest worth showing (an image tag). */
  readonly digest: boolean;
  /** The heading of the command for a chosen version: Pull command, Install command. */
  readonly commandLabel: string;
  /** Says that this format's versions are not matched against advisories (shown instead of a column of findings). */
  readonly notScannedText: string;
}

export function contentShapeOf(format: string, repositoryClass = ''): ContentShape {
  if (format === 'oci') {
    const remote = repositoryClass === 'remote';
    return {
      itemsLabel: $localize`:@@repo.content.oci.items:Images`,
      itemLabel: $localize`:@@repo.content.oci.item:Image`,
      versionsLabel: $localize`:@@repo.content.oci.versions:Tags`,
      versionLabel: $localize`:@@repo.content.oci.version:Tag`,
      filterLabel: $localize`:@@repo.content.oci.filter:Filter images`,
      emptyText: remote
        ? $localize`:@@repo.content.oci.empty-remote2:This repository has cached no images yet. Pull an image through it with your client.`
        : $localize`:@@repo.content.oci.empty2:This repository holds no images yet. Push an image with your client.`,
      emptyFilteredText: $localize`:@@repo.content.oci.empty-filtered:No image matches the filter.`,
      publishedLabel: remote
        ? $localize`:@@repo.content.oci.fetched:Fetched`
        : $localize`:@@repo.content.oci.published:Pushed`,
      digest: true,
      commandLabel: $localize`:@@repo.content.oci.command:Pull command`,
      notScannedText: $localize`:@@repo.content.no-scanning:This package type is not scanned for vulnerabilities yet.`,
    };
  }
  if (format === 'generic') {
    return { ...contentShapeOf(''), itemsLabel: $localize`:@@repo.content.files:Files` };
  }
  return {
    itemsLabel: $localize`:@@repo.content.items:Packages`,
    itemLabel: $localize`:@@repo.content.item:Package`,
    versionsLabel: $localize`:@@repo.content.versions:Versions`,
    versionLabel: $localize`:@@repo.content.version:Version`,
    filterLabel: $localize`:@@repo.content.filter:Filter packages`,
    emptyText: $localize`:@@repo.content.empty2:This repository holds no packages yet. Publish a package with your client.`,
    emptyFilteredText: $localize`:@@repo.content.empty-filtered:No package matches the filter.`,
    publishedLabel: $localize`:@@repo.content.published:Published`,
    digest: false,
    commandLabel: $localize`:@@repo.content.command:Install command`,
    notScannedText: $localize`:@@repo.content.no-scanning:This package type is not scanned for vulnerabilities yet.`,
  };
}
