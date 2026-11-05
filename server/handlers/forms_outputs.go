package serverhandlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"

	"github.com/opensvc/oc3/cdb"
	"github.com/opensvc/oc3/util/egress"
	"github.com/opensvc/oc3/util/mail"
)

// formattedData returns the data an output works on: the submitted data, or
// its JSON string form for a json-typed output, as get_form_formatted_data().
func (s *formSubmission) formattedData(output map[string]any) (any, error) {
	if s.data == nil {
		return nil, errors.New("no form data")
	}
	if defString(output, "Type") == "json" {
		b, err := json.Marshal(s.data)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	}
	return s.data, nil
}

// outputDB inserts the data in a table of the collector database, keeping only
// the keys that are columns of the table, as output_db() does.
func (s *formSubmission) outputDB(ctx context.Context, output map[string]any) error {
	outputID := defString(output, "Id")
	data, ok := s.data.(map[string]any)
	if !ok {
		return errors.New("a db output expects the form data as an object")
	}
	table := defString(output, "Table")
	if table == "" {
		s.formLog(ctx, outputID, 1, "form.submit", "Table must be set in db type Output", nil)
		return nil
	}
	cols, err := s.a.ODB.TableColumns(ctx, table)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		s.formLog(ctx, outputID, 1, "form.submit", "Table %(t)s not found", map[string]any{"t": table})
		return nil
	}
	row := map[string]any{}
	for _, col := range cols {
		if v, ok := data[col]; ok {
			if m, isMap := v.(map[string]any); isMap {
				b, _ := json.Marshal(m)
				v = string(b)
			} else if l, isList := v.([]any); isList {
				b, _ := json.Marshal(l)
				v = string(b)
			} else if n, isNumber := v.(json.Number); isNumber {
				v = n.String()
			}
			row[col] = v
		}
	}
	if err := s.a.ODB.InsertRow(ctx, table, row); err != nil {
		s.formLog(ctx, outputID, 1, "form.submit", "Data insertion in database table error: %(err)s", map[string]any{"err": err.Error()})
		return nil
	}
	s.formLog(ctx, outputID, 0, "form.submit", "Data inserted in database table", nil)
	return nil
}

// outputScript runs a script of the collector host, as output_script() does:
// the data, the output id and the results structure are its arguments, each
// line it writes is logged as it comes, stdout as info and stderr as error, and
// its exit code adds to the return code.
func (s *formSubmission) outputScript(ctx context.Context, output map[string]any) error {
	path := defString(output, "Path")
	outputID := defString(output, "Id")
	if outputID == "" {
		outputID = path
	}
	if path == "" {
		s.formLog(ctx, outputID, 1, "form.submit", "Path must be set in script type Output", nil)
		s.addReturnCode(1)
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		s.formLog(ctx, outputID, 1, "form.submit", "Script %(path)s does not exists", map[string]any{"path": path})
		s.addReturnCode(1)
		return nil
	}
	data, err := s.formattedData(output)
	if err != nil {
		return err
	}
	arg, ok := data.(string)
	if !ok {
		b, _ := json.Marshal(data)
		arg = string(b)
	}
	s.mu.Lock()
	resultsJSON, _ := json.Marshal(s.results)
	s.mu.Unlock()

	cmd := exec.CommandContext(ctx, path, arg, outputID, string(resultsJSON))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		s.formLog(ctx, outputID, 1, "form.submit", "Script %(path)s execution error: %(err)s", map[string]any{"path": path, "err": err.Error()})
		s.addReturnCode(1)
		return nil
	}
	var wg sync.WaitGroup
	stream := func(r io.Reader, ret int) {
		defer wg.Done()
		reader := bufio.NewReader(r)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				s.formLog(ctx, outputID, ret, "form.submit", line, nil)
				s.save(ctx, true)
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go stream(stdout, 0)
	go stream(stderr, 1)
	wg.Wait()
	code := 0
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			s.formLog(ctx, outputID, 1, "form.submit", "Script %(path)s execution error: %(err)s", map[string]any{"path": path, "err": err.Error()})
			s.addReturnCode(1)
			return nil
		}
		code = exitErr.ExitCode()
	}
	s.addReturnCode(code)
	if code != 0 {
		s.formLog(ctx, outputID, 1, "form.submit", "Script returned error code %(ret)s", map[string]any{"ret": fmt.Sprint(code)})
	}
	s.save(ctx, false)
	return nil
}

// mangle runs the mangler of a rest output (runMangler): its result replaces
// the data sent.
func (s *formSubmission) mangle(ctx context.Context, mangler string) (any, error) {
	s.mu.Lock()
	outputs, err := json.Marshal(s.results["outputs"])
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(s.data)
	if err != nil {
		return nil, err
	}
	return runMangler(ctx, mangler, data, outputs, s.resultsID, viper.GetDuration("server.forms.mangle_timeout"))
}

// outputRest calls the collector API as the submitter, or an external url, as
// output_rest() does: "#key" references of the url replaced, the data mangled
// or trimmed to Keys, and the answer's info, error and data kept in the
// results.
func (s *formSubmission) outputRest(ctx context.Context, output map[string]any) error {
	action := defString(output, "Handler")
	url := defString(output, "Function")
	outputID := defString(output, "Id")
	wait := 0
	if n, ok := output["WaitResult"].(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			wait = int(i)
		}
	} else if f, ok := output["WaitResult"].(float64); ok {
		wait = int(f)
	}

	s.formLog(ctx, outputID, 0, "form.submit", "rest %(action)s %(url)s", map[string]any{"action": action, "url": url})
	s.save(ctx, false)

	if url == "" {
		return errors.New("Function must be defined in a rest output")
	}
	switch action {
	case http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodPut:
	case http.MethodPatch:
		// The connectors know PATCH; the historical rest outputs did not.
		if defString(output, "Connector") == "" {
			return errors.New("Handler must be set to either GET, POST, DELETE or PUT in a rest output")
		}
	default:
		return errors.New("Handler must be set to either GET, POST, DELETE, PUT or, through a connector, PATCH in a rest output")
	}
	args, err := formRestArgs(url, s.data)
	if err != nil {
		return fmt.Errorf("%s", err)
	}
	if len(args) == 0 {
		return errors.New("Function must be defined in a rest output")
	}

	vars := s.data
	if mangler := defString(output, "Mangle"); mangler != "" {
		if vars, err = s.mangle(ctx, mangler); err != nil {
			return err
		}
	}
	if _, isString := vars.(string); isString {
		return errors.New("The mangler must not return a str")
	}
	if keys := defList(output, "Keys"); len(keys) > 0 {
		if m, ok := vars.(map[string]any); ok {
			retained := map[string]any{}
			for _, k := range keys {
				if ks, ok := k.(string); ok {
					if v, found := m[ks]; found {
						retained[ks] = v
					}
				}
			}
			vars = retained
		}
	}
	logRequest := true
	if v, ok := output["LogRequestData"].(bool); ok {
		logRequest = v
	}
	if logRequest {
		s.mu.Lock()
		ensureMap(s.results, "request_data")[outputID] = vars
		s.mu.Unlock()
	}
	s.save(ctx, false)

	if defString(output, "Connector") != "" {
		if err := s.outputConnector(ctx, output, action, vars); err != nil {
			s.formLog(ctx, outputID, 1, "form.submit", "error:\n%(result)s", map[string]any{"result": err.Error()})
			s.addReturnCode(1)
		}
		return nil
	}
	external := strings.HasPrefix(args[0], "http")
	call := func() (*apiResponse, error) {
		if external {
			full := args[0] + "//" + strings.Join(args[1:], "/")
			return s.callExternal(ctx, action, vars, full)
		}
		return s.callAPI(ctx, action, "/"+strings.Join(args, "/"), vars)
	}

	var resp *apiResponse
	if action == http.MethodGet && wait > 0 {
		for ; wait > 0; wait-- {
			if resp, err = call(); err != nil {
				break
			}
			if m, ok := resp.body.(map[string]any); ok {
				if list, ok := m["data"].([]any); ok && len(list) > 0 {
					break
				}
			}
			resp = nil
			time.Sleep(time.Second)
		}
		if err == nil && resp == nil {
			err = errors.New("Timed out waiting for a result")
		}
	} else {
		resp, err = call()
	}
	if err == nil && !external && resp.status >= 300 {
		err = errors.New(problemTextOf(resp))
	}
	if err != nil {
		s.formLog(ctx, outputID, 1, "form.submit", "error:\n%(result)s", map[string]any{"result": err.Error()})
		s.addReturnCode(1)
		return nil
	}

	// The call may have changed the results, through PUT /form_output_results.
	s.reload(ctx)
	s.restPostRun(output, outputID, resp.body)
	return nil
}

// restPostRun keeps the answer of a rest call in the results, as post_run()
// does: info lines logged, error lines logged with the return code raised, and
// data added to the output.
func (s *formSubmission) restPostRun(output map[string]any, outputID string, body any) {
	jd, ok := body.(map[string]any)
	if !ok {
		return
	}
	switch info := jd["info"].(type) {
	case []any:
		for _, line := range info {
			s.appendLog(outputID, 0, formValueText(line), nil)
		}
	case string:
		if info != "" {
			s.appendLog(outputID, 0, info, nil)
		}
	}
	if errValue, ok := jd["error"]; ok && !isFalsy(errValue) {
		s.addReturnCode(1)
		switch e := errValue.(type) {
		case []any:
			for _, line := range e {
				s.appendLog(outputID, 1, formValueText(line), nil)
			}
		case map[string]any:
			for k, v := range e {
				s.appendLog(outputID, 1, k+": "+formValueText(v), nil)
			}
		default:
			s.appendLog(outputID, 1, formValueText(e), nil)
		}
	}
	data, ok := jd["data"]
	if !ok {
		return
	}
	s.mu.Lock()
	outputs := ensureMap(s.results, "outputs")
	switch current := outputs[outputID].(type) {
	case nil:
		outputs[outputID] = data
	case []any:
		outputs[outputID] = append(current, data)
	default:
		outputs[outputID] = []any{current, data}
	}
	s.mu.Unlock()

	waited := false
	switch w := output["WaitResult"].(type) {
	case json.Number:
		n, _ := w.Int64()
		waited = n > 0
	case float64:
		waited = w > 0
	}
	if !waited {
		return
	}
	entries, isList := data.([]any)
	if !isList {
		entries = []any{data}
	}
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if ret, ok := m["ret"]; ok && formValueText(ret) != "0" {
			s.addReturnCode(1)
		}
		if stderr, ok := m["stderr"].(string); ok && stderr != "" {
			s.appendLog(outputID, 1, stderr, nil)
		}
	}
}

// outputMail mails the submitted data, as output_mail() does: to the output
// recipients, or to the given ones, a recipient given by name resolved to the
// user's email. The SMTP server is set by server.mail.server.
func (s *formSubmission) outputMail(ctx context.Context, output map[string]any, to []string, recordID int64) error {
	outputID := defString(output, "Id")
	if to == nil {
		switch t := output["To"].(type) {
		case string:
			to = []string{t}
		case []any:
			for _, item := range t {
				if str, ok := item.(string); ok {
					to = append(to, str)
				}
			}
		}
	}
	var recipients []string
	for _, t := range to {
		if strings.Contains(t, "@") {
			recipients = append(recipients, t)
			continue
		}
		email, found, err := s.a.ODB.UserEmailByName(ctx, t)
		if err != nil {
			return err
		}
		if found && email != "" {
			recipients = append(recipients, email)
		}
	}
	if len(recipients) == 0 {
		s.formLog(ctx, outputID, 1, "form.submit", "No mail destination", nil)
		return nil
	}
	if s.data == nil {
		s.formLog(ctx, outputID, 1, "form.submit", "no form data", nil)
		return nil
	}
	title := defString(s.definition, "Label")
	if title == "" {
		title = s.formName
	}
	dump, _ := json.MarshalIndent(s.data, "", "    ")
	next := ""
	if recordID != 0 {
		next = fmt.Sprintf("<p>Workflow step %d</p>", recordID)
	}
	body := fmt.Sprintf("<html><body><p>Form submitted on %s by %s</p><pre>%s</pre>%s</body></html>",
		time.Now().Format("2006-01-02 15:04"), html.EscapeString(s.caller.name), html.EscapeString(string(dump)), next)
	if err := sendFormMail(recipients, title, body); err != nil {
		s.formLog(ctx, outputID, 1, "form.submit", "Mail sending error: %(err)s", map[string]any{"err": err.Error()})
		return nil
	}
	s.formLog(ctx, outputID, 0, "form.submit", "Mail sent to %(to)s on form %(form_name)s submission.",
		map[string]any{"to": strings.Join(recipients, ", "), "form_name": s.formName})
	return nil
}

// sendFormMail sends an html mail through the SMTP server of server.mail:
// server (host:port), sender and, optionally, username and password, or the
// historical login "user:password", and security (auto, starttls, tls, none).
func sendFormMail(to []string, subject, body string) error {
	cfg := mail.ConfigFromViper("server.mail")
	if !cfg.IsSet() {
		return errors.New("mail is not configured (server.mail.server, server.mail.sender)")
	}
	return mail.Send(context.Background(), cfg, mail.Message{To: to, Subject: subject, Text: htmlToText(body), HTML: body})
}

// htmlToText returns the text of an html body, for the mail readers showing
// no html.
func htmlToText(s string) string {
	s = strings.NewReplacer("</p>", "\n", "<br>", "\n", "</pre>", "\n").Replace(s)
	return html.UnescapeString(htmlTag.ReplaceAllString(s, ""))
}

var htmlTag = regexp.MustCompile(`<[^>]*>`)

// outputWorkflow stores the submitted form as a workflow step, as
// output_workflow() does: a new workflow, or the next step of the workflow the
// submission continues (prev_wfid). The workflow stays pending while next forms
// are defined, and the assignee may be mailed.
func (s *formSubmission) outputWorkflow(ctx context.Context, output map[string]any) error {
	outputID := defString(output, "Id")
	if s.form == nil {
		return errors.New("an internal form cannot store a workflow step")
	}
	data, err := s.formattedData(output)
	if err != nil {
		return err
	}
	formData, ok := data.(string)
	if !ok {
		b, _ := json.Marshal(data)
		formData = string(b)
	}
	formMD5, err := s.a.ODB.InsertFormRevisionMD5(ctx, s.form)
	if err != nil {
		return err
	}

	stepDefs := output
	if scripts, ok := output["Scripts"].(map[string]any); ok {
		key := "Success"
		if s.returnCode() != 0 {
			key = "Error"
		}
		stepDefs, _ = scripts[key].(map[string]any)
	}
	var nextForms []any
	assignee := ""
	if stepDefs != nil {
		nextForms = defList(stepDefs, "NextForms")
		assignee = defString(stepDefs, "NextAssignee")
	}
	var nextID *int64
	status := "pending"
	if len(nextForms) == 0 {
		zero := int64(0)
		nextID = &zero
		status = "closed"
	}

	now := time.Now().Format(time.DateTime)
	primaryGroup := ""
	if s.caller.id > 0 {
		if role, found, err := s.a.ODB.UserPrimaryOrgGroupRole(ctx, s.caller.id); err != nil {
			return err
		} else if found {
			primaryGroup = role
		}
	}

	var recordID, workflowID int64
	if s.prevWfid != nil {
		prev, err := s.a.ODB.StoredFormLinkByID(ctx, *s.prevWfid)
		if err != nil {
			return err
		}
		if prev == nil {
			return fmt.Errorf("workflow step %d not found", *s.prevWfid)
		}
		if prev.NextID != nil {
			s.formLog(ctx, outputID, 0, "form.store", "This step is already completed (id=%(id)d)", map[string]any{"id": prev.ID})
			return nil
		}
		if assignee == "" {
			assignee = primaryGroup
		}
		if assignee == "" {
			assignee = prev.Submitter
		}
		if assignee == "" {
			assignee = s.caller.name
		}

		// Walk back to the head of the workflow.
		headID := *s.prevWfid
		var head *cdb.StoredFormLink
		iter := 0
		for iter < 100 {
			iter++
			row, err := s.a.ODB.StoredFormLinkByID(ctx, headID)
			if err != nil {
				return err
			}
			if row == nil {
				break
			}
			if row.PrevID == nil {
				head = row
				break
			}
			headID = *row.PrevID
		}

		if recordID, err = s.a.ODB.InsertStoredForm(ctx, cdb.StoredFormInsert{
			MD5: formMD5, Submitter: s.caller.name, Assignee: assignee, Date: now,
			PrevID: s.prevWfid, NextID: nextID, HeadID: &headID, Data: formData, ResultsID: s.resultsID,
		}); err != nil {
			return err
		}
		if err := s.a.ODB.SetStoredFormNext(ctx, *s.prevWfid, recordID); err != nil {
			return err
		}
		if nextID == nil {
			s.formLog(ctx, outputID, 0, "form.store", "Workflow %(head_id)d step %(form_name)s added with id %(id)d",
				map[string]any{"form_name": s.formName, "head_id": headID, "id": recordID})
		} else {
			s.formLog(ctx, outputID, 0, "form.store", "Workflow %(head_id)d closed on last step %(form_name)s with id %(id)d",
				map[string]any{"form_name": s.formName, "head_id": headID, "id": recordID})
		}
		w := cdb.WorkflowInsert{
			Status: status, MD5: formMD5, Steps: iter + 1, LastAssignee: assignee, LastUpdate: now,
			LastFormID: recordID, LastFormName: s.formName, HeadID: headID,
		}
		id, found, err := s.a.ODB.WorkflowIDByHead(ctx, headID)
		if err != nil {
			return err
		}
		if found {
			workflowID = id
			if err := s.a.ODB.UpdateWorkflowStep(ctx, headID, w); err != nil {
				return err
			}
		} else {
			// Should not happen: the workflow is recreated from its head.
			w.Creator, w.CreateDate = s.caller.name, now
			if head != nil {
				w.Creator, w.CreateDate = head.Submitter, head.SubmitDate
			}
			if workflowID, err = s.a.ODB.InsertWorkflow(ctx, w); err != nil {
				return err
			}
		}
	} else {
		if assignee == "" {
			assignee = primaryGroup
		}
		if assignee == "" {
			assignee = s.caller.name
		}
		if recordID, err = s.a.ODB.InsertStoredForm(ctx, cdb.StoredFormInsert{
			MD5: formMD5, Submitter: s.caller.name, Assignee: assignee, Date: now,
			Data: formData, ResultsID: s.resultsID,
		}); err != nil {
			return err
		}
		if err := s.a.ODB.SetStoredFormHead(ctx, recordID); err != nil {
			return err
		}
		s.formLog(ctx, outputID, 0, "form.store", "New workflow %(form_name)s created with id %(id)d",
			map[string]any{"form_name": s.formName, "id": recordID})
		if workflowID, err = s.a.ODB.InsertWorkflow(ctx, cdb.WorkflowInsert{
			Status: status, MD5: formMD5, Steps: 1, LastAssignee: assignee, LastUpdate: now,
			LastFormID: recordID, LastFormName: s.formName, HeadID: recordID,
			Creator: s.caller.name, CreateDate: now,
		}); err != nil {
			return err
		}
	}

	s.mu.Lock()
	ensureMap(s.results, "outputs")[outputID] = map[string]any{"workflow_id": workflowID, "head_form_id": recordID}
	s.mu.Unlock()
	s.save(ctx, false)

	if nextID == nil && defBool(output, "Mail") {
		return s.outputMail(ctx, output, []string{assignee}, recordID)
	}
	return nil
}

// callExternal calls the absolute url of a rest output, as the historical
// collector did, on a host server.forms.external_hosts lists only, through a
// client refusing the loopback, private and reserved addresses the networks of
// server.forms.external_networks do not cover (util/egress), without redirect,
// within server.forms.external_timeout and server.forms.external_max_bytes. The
// submitter's credentials are never sent. The data goes as the JSON body, a GET
// included, as before.
func (s *formSubmission) callExternal(ctx context.Context, method string, vars any, rawURL string) (*apiResponse, error) {
	u, err := neturl.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid external url %s", rawURL)
	}
	if !formsExternalHostAllowed(u) {
		return nil, fmt.Errorf("the external host %s is not allowed: call it through a connector, or list it in server.forms.external_hosts", u.Host)
	}
	networks, err := egress.ParsePrefixes(viper.GetStringSlice("server.forms.external_networks"))
	if err != nil {
		return nil, fmt.Errorf("server.forms.external_networks: %w", err)
	}
	client := egress.NewClient(egress.Options{
		Policy:  egress.Policy{Allowed: networks},
		Timeout: viper.GetDuration("server.forms.external_timeout"),
	})
	var body io.Reader
	if vars != nil {
		b, err := json.Marshal(vars)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := egress.ReadLimited(resp.Body, viper.GetInt64("server.forms.external_max_bytes"))
	if err != nil {
		return nil, err
	}
	out := &apiResponse{status: resp.StatusCode, raw: raw}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&out.body)
	return out, nil
}

// formsExternalHostAllowed tells whether server.forms.external_hosts lists the
// host of u, by name or by name and port.
func formsExternalHostAllowed(u *neturl.URL) bool {
	host := strings.ToLower(u.Hostname())
	hostPort := strings.ToLower(u.Host)
	for _, allowed := range viper.GetStringSlice("server.forms.external_hosts") {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed != "" && (allowed == host || allowed == hostPort) {
			return true
		}
	}
	return false
}
