import unittest

from measure_app_pool import measure
from compare_app_pool import compare


def node(number):
    return {"ID": f"{number:08x}-1111-1111-1111-111111111111", "NodePool": "app",
            "Status": "ready", "SchedulingEligibility": "eligible"}


def allocation(number, target):
    return {"ID": f"{number:08x}-2222-2222-2222-222222222222", "NodeID": target["ID"],
            "ClientStatus": "running"}


def detail(summary):
    return {**summary, "JobID": "hello-norn-mysql", "Namespace": "norn-work-staging", "DesiredStatus": "run",
            "Job": {"NodePool": "app"}, "TaskStates": {"web": {"State": "running"}},
            "DeploymentStatus": {"Healthy": True}}


class MeasureTests(unittest.TestCase):
    def test_two_and_three_nodes_keep_distinct_two_replica_placement(self):
        nodes = [node(1), node(2), node(3)]
        allocations = [allocation(1, nodes[0]), allocation(2, nodes[1])]
        details = [detail(a) for a in allocations]
        self.assertEqual(measure(nodes[:2], allocations, details, 2)["appNodeCount"], 2)
        self.assertEqual(measure(nodes, allocations, details, 3)["appNodeCount"], 3)

    def test_rejects_ineligible_and_collocated_nodes(self):
        nodes = [node(1), node(2)]
        allocations = [allocation(1, nodes[0]), allocation(2, nodes[0])]
        with self.assertRaises(ValueError):
            measure(nodes, allocations, [detail(a) for a in allocations], 2)
        nodes[1]["SchedulingEligibility"] = "ineligible"
        allocations = [allocation(1, nodes[0]), allocation(2, nodes[1])]
        with self.assertRaises(ValueError):
            measure(nodes, allocations, [detail(a) for a in allocations], 2)

    def test_running_client_without_healthy_task_does_not_pass(self):
        nodes = [node(1), node(2)]
        allocations = [allocation(1, nodes[0]), allocation(2, nodes[1])]
        details = [detail(a) for a in allocations]
        details[1]["DeploymentStatus"]["Healthy"] = False
        with self.assertRaisesRegex(ValueError, "health"):
            measure(nodes, allocations, details, 2)

    def test_comparison_requires_real_expansion_and_contraction(self):
        def receipt(phase, count, ids, time):
            return {"phase": phase, "appNodeCount": count, "readyEligibleAppNodeIDs": ids,
                    "namespace": "norn-work-staging", "job": "hello-norn-mysql", "observedAt": time}
        a = receipt("before", 2, [node(1)["ID"], node(2)["ID"]], "2026-10-17T01:00:00Z")
        b = receipt("expanded", 3, [node(1)["ID"], node(2)["ID"], node(3)["ID"]], "2026-10-17T02:00:00Z")
        c = receipt("contracted", 2, [node(1)["ID"], node(2)["ID"]], "2026-10-17T03:00:00Z")
        self.assertEqual(compare(a, b, c)["addedNodeIDs"], [node(3)["ID"]])
        with self.assertRaises(ValueError):
            compare(a, b, {**c, "readyEligibleAppNodeIDs": [node(4)["ID"], node(5)["ID"]]})


if __name__ == "__main__":
    unittest.main()
