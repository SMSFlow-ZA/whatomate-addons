-- An older binary does not know protected SMS connection exclusions or owners.
-- Refuse a generic legacy rehearsal when restored carrier state needs migration.
DO $$
DECLARE relation_name text; populated boolean;
BEGIN
 FOREACH relation_name IN ARRAY ARRAY['addon_carrier_ingress','addon_carrier_route','addon_carrier_receipt','addon_carrier_sms_cursor','addon_carrier_case_scan','addon_delivery_erased'] LOOP
  IF to_regclass(relation_name) IS NOT NULL THEN
   EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I)',relation_name) INTO populated;
   IF populated THEN
    RAISE EXCEPTION 'Protected carrier state requires a reviewed carrier-aware rollback; legacy rehearsal refused';
   END IF;
  END IF;
 END LOOP;
END $$;
