"""tkt wrapper tests — subprocess fully mocked so no real tkt/board is needed."""
import json
import subprocess
from unittest import mock

import pytest

from tktban.tkt import Tkt, TktError


def _proc(returncode=0, stdout="", stderr=""):
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


@mock.patch("tktban.tkt.subprocess.run")
def test_roles_argv_and_parse(run):
    run.return_value = _proc(stdout='{"todo": "To Do", "done": "Done"}')
    t = Tkt(binary="tkt")
    roles = t.roles()
    assert roles == {"todo": "To Do", "done": "Done"}
    argv = run.call_args[0][0]
    assert argv == ["tkt", "cfg", "board.roles", "--json"]


@mock.patch("tktban.tkt.subprocess.run")
def test_config_passed_via_env_not_argv(run):
    run.return_value = _proc(stdout="[]")
    Tkt(config="/x/.sdlc/config.toml").list_all()
    argv = run.call_args[0][0]
    # config travels via TKT_CONFIG env, never as a --config flag (which tkt
    # clobbers when placed before the verb).
    assert argv == ["tkt", "list", "--query", "all", "--json"]
    assert "--config" not in argv
    assert run.call_args.kwargs["env"]["TKT_CONFIG"] == "/x/.sdlc/config.toml"


@mock.patch("tktban.tkt.subprocess.run")
def test_no_config_no_env_override(run):
    run.return_value = _proc(stdout="[]")
    Tkt().list_all()
    assert run.call_args.kwargs["env"] is None


@mock.patch("tktban.tkt.subprocess.run")
def test_list_all_returns_array(run):
    run.return_value = _proc(stdout='[{"key": "TKT-1"}, {"key": "TKT-2"}]')
    out = Tkt().list_all()
    assert [t["key"] for t in out] == ["TKT-1", "TKT-2"]


@mock.patch("tktban.tkt.subprocess.run")
def test_create_omits_empty_optionals(run):
    run.return_value = _proc(stdout='{"key": "TKT-9"}')
    out = Tkt().create("Task", "do a thing", priority="High")
    assert out["key"] == "TKT-9"
    argv = run.call_args[0][0]
    assert argv == ["tkt", "create", "--type", "Task", "--summary", "do a thing",
                    "--priority", "High", "--json"]
    assert "--assignee" not in argv and "--body" not in argv


@mock.patch("tktban.tkt.subprocess.run")
def test_edit_argv_sends_only_provided_fields(run):
    run.return_value = _proc(stdout='{"key": "TKT-1"}')
    Tkt().edit("TKT-1", summary="new", priority="High",
               add_labels=["x"], remove_labels=["y"])
    argv = run.call_args[0][0]
    assert argv == ["tkt", "edit", "TKT-1", "--summary", "new",
                    "--priority", "High", "--add-label", "x",
                    "--remove-label", "y", "--json"]
    assert "--body" not in argv and "--assignee" not in argv


@mock.patch("tktban.tkt.subprocess.run")
def test_edit_empty_string_is_sent_but_none_is_omitted(run):
    run.return_value = _proc(stdout="{}")
    Tkt().edit("TKT-1", assignee="")  # clear assignee; summary omitted
    argv = run.call_args[0][0]
    assert "--assignee" in argv and argv[argv.index("--assignee") + 1] == ""
    assert "--summary" not in argv


@mock.patch("tktban.tkt.subprocess.run")
def test_lane_time_is_read_only_and_parses(run):
    run.return_value = _proc(stdout='[{"key": "TKT-1", "human": "6h 10m", "worklog_id": ""}]')
    out = Tkt().lane_time("TKT-1", "todo")
    assert out["human"] == "6h 10m"
    argv = run.call_args[0][0]
    assert argv == ["tkt", "lane-time", "--keys", "TKT-1:todo",
                    "--read-only", "--json"]
    assert out["worklog_id"] == ""   # read-only: never records a worklog


@mock.patch("tktban.tkt.subprocess.run")
def test_lane_time_batch_is_read_only_and_maps_by_key(run):
    run.return_value = _proc(stdout='[{"key": "TKT-1", "human": "6h 10m"}, {"key": "TKT-2", "human": "1h 5m"}]')
    out = Tkt().lane_time_batch([("TKT-1", "todo"), ("TKT-2", "in_progress")])
    assert out == {
        "TKT-1": {"key": "TKT-1", "human": "6h 10m"},
        "TKT-2": {"key": "TKT-2", "human": "1h 5m"},
    }
    argv = run.call_args[0][0]
    assert argv == ["tkt", "lane-time", "--keys", "TKT-1:todo,TKT-2:in_progress",
                    "--read-only", "--json"]


@mock.patch("tktban.tkt.subprocess.run")
def test_lane_time_returns_none_on_no_history(run):
    # Benign: ticket has no entry in that lane -> wrapper degrades to None.
    run.return_value = _proc(returncode=3, stderr="no entry into 'To Do' in history")
    assert Tkt().lane_time("TKT-1", "todo") is None


@mock.patch("tktban.tkt.subprocess.run")
def test_lane_time_reraises_genuine_error(run):
    # A real provider/config failure must NOT be masked as "no badge".
    run.return_value = _proc(returncode=2, stderr="config error: bad board_dir")
    with pytest.raises(TktError):
        Tkt().lane_time("TKT-1", "todo")


@mock.patch("tktban.tkt.subprocess.run")
def test_transition_and_comment_argv(run):
    run.return_value = _proc(stdout="ok")
    t = Tkt()
    t.transition("TKT-1", "review")
    assert run.call_args[0][0] == ["tkt", "transition", "TKT-1", "review"]
    t.comment("TKT-1", "looks good")
    assert run.call_args[0][0] == ["tkt", "comment", "TKT-1", "looks good"]


@mock.patch("tktban.tkt.subprocess.run")
def test_nonzero_exit_raises_with_code_and_stderr(run):
    run.return_value = _proc(returncode=4, stderr="tkt: ticket TKT-99 not found")
    with pytest.raises(TktError) as ei:
        Tkt().view("TKT-99")
    err = ei.value
    assert err.exit_code == 4
    assert "not found" in str(err)
    assert "TKT-99" in err.stderr


@mock.patch("tktban.tkt.subprocess.run")
def test_invalid_json_raises(run):
    run.return_value = _proc(stdout="not json")
    with pytest.raises(TktError) as ei:
        Tkt().roles()
    assert "invalid JSON" in str(ei.value)


@mock.patch("tktban.tkt.subprocess.run", side_effect=FileNotFoundError())
def test_missing_binary_raises(run):
    with pytest.raises(TktError) as ei:
        Tkt(binary="nope").roles()
    assert "not found" in str(ei.value)


@mock.patch("tktban.tkt.shutil.which", return_value="/usr/bin/tkt")
@mock.patch("tktban.tkt.subprocess.run")
def test_doctor_all_green(run, which):
    run.side_effect = [
        _proc(stdout='{"todo": "To Do"}'),   # roles
        _proc(stdout="[]"),                    # list_all
    ]
    checks = Tkt().doctor()
    assert [c[1] for c in checks] == [True, True, True]


@mock.patch("tktban.tkt.shutil.which", return_value="/usr/bin/tkt")
@mock.patch("tktban.tkt.subprocess.run")
def test_doctor_missing_all_query(run, which):
    run.side_effect = [
        _proc(stdout='{"todo": "To Do"}'),                 # roles ok
        _proc(returncode=2, stderr="no [queries].all"),    # list_all fails
    ]
    checks = Tkt().doctor()
    names = {c[0]: c for c in checks}
    assert names["'all' query present"][1] is False
    assert "ORDER BY key ASC" in names["'all' query present"][2]


@mock.patch("tktban.tkt.shutil.which", return_value=None)
def test_doctor_no_binary_short_circuits(which):
    checks = Tkt(binary="nope").doctor()
    assert len(checks) == 1 and checks[0][1] is False
