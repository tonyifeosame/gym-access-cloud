/**
 * What the console calls the value a terminal reads off a person.
 *
 * ---------------------------------------------------------------------------
 * ONE NAME, BECAUSE FOUR SCREENS SHOW THE SAME FIELD
 * ---------------------------------------------------------------------------
 *
 * The API stores it as `external_id`, and the console had been calling it three
 * different things across four screens: the People table headed the column
 * "Member ID" and searched on "member ID"; the person form labelled the field
 * "Identifier" and reported conflicts against "that identifier"; the Events
 * search box said "identifier"; and the access-evaluation dialog said "Person
 * identifier" and then had to explain itself with the hint "the external id the
 * terminal would read — what People calls Identifier". A hint that translates
 * between two of the product's own words is the product admitting it has two.
 *
 * WHY NOT "MEMBER ID": ACCESSLINK IS NOT GYM SOFTWARE. The same build runs at an
 * office, a school, a warehouse and a factory, and none of those has members. A
 * supervisor reading "Member ID" over a column of employee numbers is being
 * told, quietly and on every screen, that they are using somebody else's
 * product.
 *
 * WHY NOT "IDENTIFIER": it is what the schema calls it, not what anybody calls
 * it. It names the role the value plays in our data model rather than the thing
 * the customer already has printed on a badge.
 *
 * "ID NUMBER" is what remains — the words an employee number, a student number,
 * a badge number and a contractor reference all already answer to, in every one
 * of those buildings, with nothing about the industry attached.
 *
 * THE FIELD ITSELF IS UNCHANGED. `external_id` is still the API's name, the path
 * parameter, the sync key terminals hold, and the value typed into a delete
 * confirmation. Nothing here renames anything but the words on the screen.
 */

/**
 * The field's name. Reads correctly both as a label and mid-sentence ("no
 * person with that ID number"), which is why there is one constant and not two.
 */
export const PERSON_ID_LABEL = 'ID number'

/**
 * The one-line explanation of what to put in it.
 *
 * NAMES FOUR KINDS OF ORGANISATION AND COMMITS TO NONE, which is the whole job:
 * whoever is reading recognises their own word in the list and stops wondering
 * whether the field means something else.
 */
export const PERSON_ID_HINT =
  'The badge, employee, student or reference number your organisation already uses. Unique within your company.'
