-- The rows of a package file (deb_binaries, deb_coordinates) outlive the deletion of the file's item for the length of one
-- transaction: the item store deletes the item rows first and then runs the format's OnItemsRemoved hook, which needs the file's
-- package, version, architecture and coordinates to raise the suites and to journal the deletion (R-DEB-013, R-DEB-032). With a
-- cascading foreign key the rows were gone before the hook ran. The hook deletes them itself (a permanent removal) or keeps them (a
-- file in the trash can); the rows of a repository still go with the repository.

ALTER TABLE deb_binaries DROP CONSTRAINT deb_binaries_item_id_fkey;
